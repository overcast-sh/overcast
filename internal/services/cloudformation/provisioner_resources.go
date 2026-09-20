package cloudformation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// ── AWS::IAM::Policy (inline policy) ───────────────────────────────────────

type iamPolicyHandler struct{}

// policyDocumentJSON renders a CloudFormation `Json`-typed policy property as
// the document string the IAM API takes.
//
// CloudFormation accepts the property as a JSON object *or* as a string that
// already holds JSON, and templates use both. Reading only the object form
// turned a string into `null` — silently, while IAM stored documents unparsed,
// and as a hard MalformedPolicyDocument failure once it started checking them
// (#1717). A string is passed through untouched so IAM sees exactly what the
// template author wrote; anything else is marshalled as before.
func policyDocumentJSON(v any) []byte {
	if s, ok := v.(string); ok {
		return []byte(s)
	}
	b, _ := json.Marshal(v)
	return b
}

// iamValidateInlinePolicyPrincipals enforces AWS::IAM::Policy's one documented
// cross-property rule: "The Groups, Roles, and Users properties are optional.
// However, you must specify at least one of these properties."
// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-policy.html
//
// Without it the resource reports CREATE_COMPLETE having written its document
// to nobody, so a template missing its `Roles:` line surfaces much later as a
// permission that silently does not exist.
func iamValidateInlinePolicyPrincipals(props map[string]any) error {
	total := 0
	for _, property := range []string{"Groups", "Roles", "Users"} {
		entities, err := iamStringSet(props, property)
		if err != nil {
			return err
		}
		total += len(entities)
	}
	if total == 0 {
		return fmt.Errorf("Policy: at least one of Groups, Roles or Users is required")
	}
	return nil
}

func (h *iamPolicyHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidateInlinePolicyPrincipals(props); err != nil {
		return "", nil, err
	}
	policyName, _ := props["PolicyName"].(string)
	if policyName == "" {
		policyName = rCtx.generatedNameWithin(maxNameLenIAMPolicy)
	}

	policyJSON := policyDocumentJSON(props["PolicyDocument"])

	// Write the document onto every principal the properties name. Update and
	// Delete already handled all three principal kinds; Create used to stop at
	// Roles, so a policy aimed at users or groups attached to nothing (#710).
	for _, target := range []struct{ propKey, action, param string }{
		{"Roles", "PutRolePolicy", "RoleName"},
		{"Groups", "PutGroupPolicy", "GroupName"},
		{"Users", "PutUserPolicy", "UserName"},
	} {
		entities, ok := props[target.propKey].([]any)
		if !ok {
			continue
		}
		for _, e := range entities {
			entity, _ := e.(string)
			if entity == "" {
				continue
			}
			params := map[string]string{
				"Action":         target.action,
				"Version":        "2010-05-08",
				target.param:     entity,
				"PolicyName":     policyName,
				"PolicyDocument": string(policyJSON),
			}
			if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
				return "", nil, fmt.Errorf("%s on %s: %w", target.action, entity, err)
			}
		}
	}

	physicalID := rCtx.StackName + "-" + policyName
	return physicalID, nil, nil
}

// Delete without the resource's properties cannot know which roles, users or
// groups the inline policy was written to, so it does nothing. It is only
// reached for a stack record stored before properties were persisted; a record
// that carries them goes through DeleteWithProperties.
func (h *iamPolicyHandler) Delete(_ context.Context, _ http.Handler, _ *config.Config, _ string, _ *resolveContext) error {
	return nil
}

// DeleteWithProperties removes the inline policy document from every entity the
// resource put it on, as real CloudFormation's AWS::IAM::Policy provider does.
//
// This has to happen now that IAM enforces AWS's DeleteConflict: an inline
// policy left behind makes DeleteRole/DeleteUser/DeleteGroup refuse, and
// CloudFormation deletes this resource before the role it names (the Ref
// creates the dependency edge, and teardown runs in reverse), so the leftover
// would strand the role and fail the stack teardown.
//
// An entity that is already gone took its inline policy with it, so nothing is
// reported for it. Every other failure is: the policy is still attached, and
// the DeleteRole that follows will refuse because of it.
func (h *iamPolicyHandler) DeleteWithProperties(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	policyName, _ := props["PolicyName"].(string)
	if policyName == "" {
		// Unnamed in the template: Create generated the name and folded it into
		// the physical ID, which is the only record of it.
		policyName = physicalID
		if prefix := rCtx.StackName + "-"; strings.HasPrefix(physicalID, prefix) {
			policyName = physicalID[len(prefix):]
		}
	}
	if policyName == "" {
		return nil
	}

	var removalErr error
	for _, target := range []struct{ propKey, action, param string }{
		{"Roles", "DeleteRolePolicy", "RoleName"},
		{"Groups", "DeleteGroupPolicy", "GroupName"},
		{"Users", "DeleteUserPolicy", "UserName"},
	} {
		entities, ok := props[target.propKey].([]any)
		if !ok {
			continue
		}
		for _, e := range entities {
			entity, _ := e.(string)
			if entity == "" {
				continue
			}
			rec, err := internalQuery(ctx, router, rCtx.Region, map[string]string{
				"Action":     target.action,
				"Version":    "2010-05-08",
				target.param: entity,
				"PolicyName": policyName,
			})
			// Every entity is attempted before reporting: one that refuses must
			// not leave the policy attached to the others.
			removalErr = errors.Join(removalErr, teardownError(target.action, rec, err))
		}
	}
	return removalErr
}

func (h *iamPolicyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, _ map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidateInlinePolicyPrincipals(props); err != nil {
		return "", nil, failUpdate(err)
	}
	policyName, _ := props["PolicyName"].(string)

	oldPolicyName := physicalID
	if prefix := rCtx.StackName + "-"; strings.HasPrefix(physicalID, prefix) {
		oldPolicyName = physicalID[len(prefix):]
	}
	if policyName == "" {
		// Unnamed in the template: keep the generated name from create time.
		// See iamManagedPolicyHandler.Update for why regenerating is wrong.
		policyName = oldPolicyName
	}
	if oldPolicyName != policyName {
		return "", nil, errReplacementRequired
	}

	policyJSON := policyDocumentJSON(props["PolicyDocument"])

	if roles, ok := props["Roles"].([]any); ok {
		for _, r := range roles {
			roleName, _ := r.(string)
			if roleName == "" {
				continue
			}
			params := map[string]string{
				"Action":         "PutRolePolicy",
				"Version":        "2010-05-08",
				"RoleName":       roleName,
				"PolicyName":     policyName,
				"PolicyDocument": string(policyJSON),
			}
			if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
				return "", nil, fmt.Errorf("PutRolePolicy on %s: %w", roleName, err)
			}
		}
	}

	if groups, ok := props["Groups"].([]any); ok {
		for _, g := range groups {
			groupName, _ := g.(string)
			if groupName == "" {
				continue
			}
			params := map[string]string{
				"Action":         "PutGroupPolicy",
				"Version":        "2010-05-08",
				"GroupName":      groupName,
				"PolicyName":     policyName,
				"PolicyDocument": string(policyJSON),
			}
			if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
				return "", nil, fmt.Errorf("PutGroupPolicy on %s: %w", groupName, err)
			}
		}
	}

	if users, ok := props["Users"].([]any); ok {
		for _, u := range users {
			userName, _ := u.(string)
			if userName == "" {
				continue
			}
			params := map[string]string{
				"Action":         "PutUserPolicy",
				"Version":        "2010-05-08",
				"UserName":       userName,
				"PolicyName":     policyName,
				"PolicyDocument": string(policyJSON),
			}
			if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
				return "", nil, fmt.Errorf("PutUserPolicy on %s: %w", userName, err)
			}
		}
	}

	return physicalID, nil, nil
}

// ── AWS::IAM::ManagedPolicy ────────────────────────────────────────────────

type iamManagedPolicyHandler struct{}

func (h *iamManagedPolicyHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidateManagedPolicyPrincipals(props); err != nil {
		return "", nil, err
	}
	policyName, _ := props["ManagedPolicyName"].(string)
	if policyName == "" {
		policyName = rCtx.generatedNameWithin(maxNameLenIAMPolicy)
	}
	policyJSON := policyDocumentJSON(props["PolicyDocument"])

	path := "/"
	if v, _ := props["Path"].(string); v != "" {
		path = v
	}

	params := map[string]string{
		"Action":         "CreatePolicy",
		"Version":        "2010-05-08",
		"PolicyName":     policyName,
		"PolicyDocument": string(policyJSON),
		"Path":           path,
	}
	if description, _ := props["Description"].(string); description != "" {
		params["Description"] = description
	}
	tags, err := iamEffectiveTags(props, rCtx.StackTags)
	if err != nil {
		return "", nil, err
	}
	iamTagParams(params, tags)

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreatePolicy: %w", err)
	}

	// IAM CreatePolicy returns XML with <Arn> inside <Policy>.
	// Parse the ARN from the response body.
	arn := extractXMLTag(rec.Body.String(), "Arn")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:iam::%s:policy%s%s", rCtx.AccountID, path, policyName)
	}

	attrs := map[string]string{
		"Arn": arn,
	}
	// Attach to every principal the Roles/Users/Groups lists name (#521 —
	// these used to be parsed into nothing, so a template's policy attached to
	// nobody).
	mutations, err := iamPolicyPrincipalMutations(arn, props, nil)
	if err != nil {
		return "", nil, err
	}
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(mutations); err != nil {
		if cleanupErr := iamQuery(ctx, router, rCtx.Region, "DeletePolicy", map[string]string{"PolicyArn": arn}); cleanupErr != nil {
			return "", nil, fmt.Errorf("%w; cleanup newly-created managed policy: %v", err, cleanupErr)
		}
		return "", nil, err
	}
	return arn, attrs, nil
}

func (h *iamManagedPolicyHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":    "DeletePolicy",
		"Version":   "2010-05-08",
		"PolicyArn": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return iamTeardownError("DeletePolicy", physicalID, rec, err)
}

// DeleteWithProperties detaches the policy from every principal its own
// properties attached it to before deleting it — IAM answers DeleteConflict
// while any attachment remains (#710). Attachments made outside the stack are
// left alone; if one exists, the delete still refuses and the stack fails, as
// AWS's would.
func (h *iamManagedPolicyHandler) DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	return iamPrincipalTeardown(ctx, router, rCtx, func() error {
		return h.Delete(ctx, router, cfg, physicalID, rCtx)
	}, func() ([]iamMutation, error) {
		return iamPolicyPrincipalMutations(physicalID, nil, props)
	})
}

func (h *iamManagedPolicyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidateManagedPolicyPrincipals(props); err != nil {
		return "", nil, failUpdate(err)
	}
	policyName, _ := props["ManagedPolicyName"].(string)

	// Extract policy name from ARN to detect rename.
	oldName := physicalID
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		oldName = physicalID[idx+1:]
	}
	if policyName == "" {
		// The template does not name this policy, so CloudFormation keeps the
		// name it generated at create time — an unnamed resource is not
		// renamed by an update. Generating a fresh one here would compare
		// unequal every time and force a needless replacement.
		policyName = oldName
	}
	if oldName != policyName {
		return "", nil, errReplacementRequired
	}
	// Path and Description are create-only on AWS::IAM::ManagedPolicy.
	if iamJSONPropertyChanged(props, oldProps, "Path") || iamJSONPropertyChanged(props, oldProps, "Description") {
		return "", nil, errReplacementRequired
	}

	mutations, err := iamPolicyPrincipalMutations(physicalID, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	tags, err := iamPolicyTagMutations(physicalID, props, oldProps, rCtx.StackTags, rCtx.PreviousStackTags)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	mutations = append(mutations, tags...)
	tx := newIAMTransaction(ctx, router, rCtx.Region)
	if err := tx.apply(mutations); err != nil {
		return "", nil, classifyIAMTransactionFailure(err)
	}

	// The document updates in place under the same ARN, as AWS's provider
	// does: a new default policy version.
	if iamJSONPropertyChanged(props, oldProps, "PolicyDocument") {
		policyJSON := policyDocumentJSON(props["PolicyDocument"])
		params := map[string]string{
			"PolicyArn":      physicalID,
			"PolicyDocument": string(policyJSON),
			"SetAsDefault":   "true",
		}
		if err := iamQuery(ctx, router, rCtx.Region, "CreatePolicyVersion", params); err != nil {
			if rollbackErr := tx.rollback(); rollbackErr != nil {
				return "", nil, failDirtyUpdate(fmt.Errorf("%w; rollback: %v", err, rollbackErr))
			}
			return "", nil, failUpdate(err)
		}
	}

	return physicalID, nil, nil
}

// iamValidateManagedPolicyPrincipals rejects malformed Roles/Users/Groups
// attachment lists before any mutation is dispatched.
func iamValidateManagedPolicyPrincipals(props map[string]any) error {
	for _, property := range []string{"Roles", "Users", "Groups"} {
		if _, err := iamStringSet(props, property); err != nil {
			return err
		}
	}
	return nil
}

// ── AWS::IAM::InstanceProfile ──────────────────────────────────────────────

type iamInstanceProfileHandler struct{}

func (h *iamInstanceProfileHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// A malformed Roles list fails before anything is created, so a bad
	// template fails whole rather than half-applied.
	if _, err := iamStringSet(props, "Roles"); err != nil {
		return "", nil, err
	}
	profileName, _ := props["InstanceProfileName"].(string)
	if profileName == "" {
		profileName = rCtx.generatedNameWithin(maxNameLenIAMPolicy)
	}
	path := "/"
	if v, _ := props["Path"].(string); v != "" {
		path = v
	}

	params := map[string]string{
		"Action":              "CreateInstanceProfile",
		"Version":             "2010-05-08",
		"InstanceProfileName": profileName,
		"Path":                path,
	}
	tags, err := iamEffectiveTags(props, rCtx.StackTags)
	if err != nil {
		return "", nil, err
	}
	iamTagParams(params, tags)
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateInstanceProfile: %w", err)
	}

	arn := extractXMLTag(rec.Body.String(), "Arn")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:iam::%s:instance-profile%s%s", rCtx.AccountID, path, profileName)
	}

	// Add the roles the template names. A failure here leaves no half-built
	// profile behind, the same cleanup the role and group handlers do.
	roles, err := iamInstanceProfileRoleMutations(profileName, props, nil)
	if err != nil {
		return "", nil, err
	}
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(roles); err != nil {
		if cleanupErr := iamQuery(ctx, router, rCtx.Region, "DeleteInstanceProfile", map[string]string{"InstanceProfileName": profileName}); cleanupErr != nil {
			return "", nil, fmt.Errorf("%w; cleanup newly-created instance profile: %v", err, cleanupErr)
		}
		return "", nil, err
	}

	attrs := map[string]string{
		"Arn": arn,
	}
	// "Ref returns the resource name ... Ref returns the name of the instance
	// profile"; the ARN is GetAtt "Arn" and only that. Returning the ARN as the
	// physical ID put one into every template that feeds this Ref to a property
	// AWS documents as a name — AWS::EC2::Instance's IamInstanceProfile, a
	// launch template's IamInstanceProfile.Name — which is how CDK's own
	// ec2.Instance wires it.
	// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-instanceprofile.html
	return profileName, attrs, nil
}

// instanceProfileNameFromPhysicalID reads the profile name out of a stored
// physical ID. Records written before the Create above returned the name carry
// the ARN, whose last segment is the name.
func instanceProfileNameFromPhysicalID(physicalID string) string {
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		return physicalID[idx+1:]
	}
	return physicalID
}

func (h *iamInstanceProfileHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	name := instanceProfileNameFromPhysicalID(physicalID)
	params := map[string]string{
		"Action":              "DeleteInstanceProfile",
		"Version":             "2010-05-08",
		"InstanceProfileName": name,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return iamTeardownError("DeleteInstanceProfile", name, rec, err)
}

func (h *iamInstanceProfileHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name := instanceProfileNameFromPhysicalID(physicalID)

	if oldProps != nil {
		if newProfileName, _ := props["InstanceProfileName"].(string); newProfileName != "" {
			if oldProfileName, _ := oldProps["InstanceProfileName"].(string); oldProfileName != "" && newProfileName != oldProfileName {
				return "", nil, errReplacementRequired
			}
		}
		if newPath, _ := props["Path"].(string); newPath != "" {
			if oldPath, _ := oldProps["Path"].(string); oldPath != "" && newPath != oldPath {
				return "", nil, errReplacementRequired
			}
		}
	}

	roles, err := iamInstanceProfileRoleMutations(name, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	tags, err := iamTagMutations("InstanceProfile", name, props, oldProps, rCtx.StackTags, rCtx.PreviousStackTags)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(append(roles, tags...)); err != nil {
		return "", nil, classifyIAMTransactionFailure(err)
	}

	// The physical ID is the profile name, so GetAtt "Arn" has to be rebuilt
	// rather than echoed. Path is create-only (a change forced a replacement
	// above), so the template's current value is the one the profile was made
	// with.
	attrs := map[string]string{"Arn": iamPathedARN(rCtx.AccountID, "instance-profile", props, name)}
	return name, attrs, nil
}

// iamPathedARN renders an IAM ARN for an entity whose Path is a create-only
// property, so an update can restate it without another round trip. An absent
// Path is AWS's "/" default.
func iamPathedARN(accountID, resourceType string, props map[string]any, name string) string {
	path := "/"
	if v, _ := props["Path"].(string); v != "" {
		path = v
	}
	return fmt.Sprintf("arn:aws:iam::%s:%s%s%s", accountID, resourceType, path, name)
}

// DeleteWithProperties takes the profile's roles back out before deleting it,
// as real CloudFormation's provider does: IAM refuses DeleteInstanceProfile
// while an association remains, and the Roles list is the only record of what
// this resource put there. A role that is already gone took the association
// with it, so NoSuchEntity is not an error here.
func (h *iamInstanceProfileHandler) DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	name := instanceProfileNameFromPhysicalID(physicalID)
	return iamPrincipalTeardown(ctx, router, rCtx, func() error {
		return h.Delete(ctx, router, cfg, physicalID, rCtx)
	}, func() ([]iamMutation, error) {
		return iamInstanceProfileRoleMutations(name, nil, props)
	})
}

// iamInstanceProfileRoleMutations reconciles the Roles property via
// AddRoleToInstanceProfile / RemoveRoleFromInstanceProfile. Create passes
// oldProps == nil (everything is an add); teardown passes props == nil
// (everything is a remove).
func iamInstanceProfileRoleMutations(profileName string, props, oldProps map[string]any) ([]iamMutation, error) {
	desired, err := iamStringSet(props, "Roles")
	if err != nil {
		return nil, err
	}
	previous, err := iamStringSet(oldProps, "Roles")
	if err != nil {
		return nil, err
	}
	mutations := make([]iamMutation, 0, len(desired)+len(previous))
	// Removals come first: an instance profile holds at most one role, so a
	// swap that added before removing would be refused by the quota.
	for role := range previous {
		if _, remains := desired[role]; remains {
			continue
		}
		params := map[string]string{"InstanceProfileName": profileName, "RoleName": role}
		mutations = append(mutations, iamMutation{
			action: "RemoveRoleFromInstanceProfile", params: params,
			undoAction: "AddRoleToInstanceProfile", undoParams: params,
		})
	}
	for role := range desired {
		if _, existed := previous[role]; existed {
			continue
		}
		params := map[string]string{"InstanceProfileName": profileName, "RoleName": role}
		mutations = append(mutations, iamMutation{
			action: "AddRoleToInstanceProfile", params: params,
			undoAction: "RemoveRoleFromInstanceProfile", undoParams: params,
		})
	}
	return mutations, nil
}

// ── AWS::IAM::ServiceLinkedRole ────────────────────────────────────────────

type iamServiceLinkedRoleHandler struct{}

func (h *iamServiceLinkedRoleHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	serviceName, _ := props["AWSServiceName"].(string)
	if serviceName == "" {
		return "", nil, fmt.Errorf("ServiceLinkedRole: AWSServiceName is required")
	}

	// Derive role name from service: e.g. elasticloadbalancing.amazonaws.com → AWSServiceRoleForElasticLoadBalancing
	roleName := "AWSServiceRoleFor" + serviceName
	if idx := strings.Index(roleName, "."); idx >= 0 {
		roleName = roleName[:idx]
	}

	assumePolicy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"%s"},"Action":"sts:AssumeRole"}]}`, serviceName)

	params := map[string]string{
		"Action":                   "CreateRole",
		"Version":                  "2010-05-08",
		"RoleName":                 roleName,
		"Path":                     "/aws-service-role/" + serviceName + "/",
		"AssumeRolePolicyDocument": assumePolicy,
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateServiceLinkedRole: %w", err)
	}

	arn := extractXMLTag(rec.Body.String(), "Arn")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:iam::%s:role/aws-service-role/%s/%s", rCtx.AccountID, serviceName, roleName)
	}

	attrs := map[string]string{
		"Arn":      arn,
		"RoleName": roleName,
	}
	return arn, attrs, nil
}

func (h *iamServiceLinkedRoleHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	roleName := physicalID
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		roleName = physicalID[idx+1:]
	}
	params := map[string]string{
		"Action":   "DeleteRole",
		"Version":  "2010-05-08",
		"RoleName": roleName,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return iamTeardownError("DeleteRole", roleName, rec, err)
}

// ── AWS::Events::EventBus ──────────────────────────────────────────────────

type eventsEventBusHandler struct{}

func (h *eventsEventBusHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedName()
	}

	body := map[string]any{"Name": name}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSEvents.CreateEventBus", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateEventBus: %w", err)
	}

	var resp struct {
		EventBusArn string `json:"EventBusArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateEventBus: parse response: %w", err)
	}

	arn := resp.EventBusArn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:events:%s:%s:event-bus/%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"Arn":  arn,
		"Name": name,
	}
	// The physical ID is the bus *name*: AWS documents Ref on
	// AWS::Events::EventBus as returning the name, and every consumer builds an
	// ARN as "…:event-bus/" + name. Returning the ARN here fed it back into that
	// concatenation and produced a doubled ARN.
	return name, attrs, nil
}

func (h *eventsEventBusHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Extract name from ARN.
	name := physicalID
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		name = physicalID[idx+1:]
	}
	body := map[string]any{"Name": name}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSEvents.DeleteEventBus", body)
	return teardownError("DeleteEventBus", rec, err)
}

// ── AWS::Events::Rule ──────────────────────────────────────────────────────

type eventsRuleHandler struct{}

func (h *eventsRuleHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := make(map[string]any)
	// Name is optional on AWS::Events::Rule and CDK's `events.Rule` never sets
	// it. PutRule accepted the empty name, so every unnamed rule in a stack
	// became the *same* nameless rule and the stack still reported success.
	if v, _ := props["Name"].(string); v != "" {
		body["Name"] = v
	} else {
		body["Name"] = rCtx.generatedNameWithin(maxNameLenEvents)
	}
	if v, _ := props["EventBusName"].(string); v != "" {
		body["EventBusName"] = v
	}
	if v, _ := props["State"].(string); v != "" {
		body["State"] = v
	} else {
		body["State"] = "ENABLED"
	}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if v, _ := props["RoleArn"].(string); v != "" {
		body["RoleArn"] = v
	}
	if v, _ := props["EventPattern"].(map[string]any); v != nil {
		j, _ := json.Marshal(v)
		body["EventPattern"] = string(j)
	} else if v, _ := props["EventPattern"].(string); v != "" {
		body["EventPattern"] = v
	}
	if v, _ := props["ScheduleExpression"].(string); v != "" {
		body["ScheduleExpression"] = v
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSEvents.PutRule", body)
	if err != nil {
		return "", nil, fmt.Errorf("PutRule: %w", err)
	}

	var resp struct {
		RuleArn string `json:"RuleArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("PutRule: parse response: %w", err)
	}

	attrs := map[string]string{
		"Arn": resp.RuleArn,
	}
	ruleName, _ := body["Name"].(string)
	if ruleName == "" {
		ruleName = eventRuleNameFromArn(resp.RuleArn)
	}
	if targets, _ := props["Targets"].([]any); len(targets) > 0 {
		eventBusName, _ := body["EventBusName"].(string)
		if err := putEventTargets(ctx, router, rCtx.Region, ruleName, eventBusName, targets); err != nil {
			return "", nil, err
		}
	}
	return resp.RuleArn, attrs, nil
}

// putEventTargets adds targets to an EventBridge rule and fails the resource
// when EventBridge rejects any of them.
//
// A rejected target names a target type this emulator cannot deliver to.
// Ignoring the rejection would leave the stack provisioned with a rule that
// can never fire — exactly the silent failure issue #467 removes, and what
// docs/plans/full-emulation-priority.md §2.1 forbids.
func putEventTargets(ctx context.Context, router http.Handler, region, ruleName, eventBusName string, targets []any) error {
	body := map[string]any{"Rule": ruleName, "Targets": targets}
	if eventBusName != "" {
		body["EventBusName"] = eventBusName
	}
	rec, err := internalJSON(ctx, router, region, "AWSEvents.PutTargets", body)
	if err != nil {
		return fmt.Errorf("PutTargets: %w", err)
	}
	var resp struct {
		FailedEntryCount int `json:"FailedEntryCount"`
		FailedEntries    []struct {
			TargetID     string `json:"TargetId"`
			ErrorCode    string `json:"ErrorCode"`
			ErrorMessage string `json:"ErrorMessage"`
		} `json:"FailedEntries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return fmt.Errorf("PutTargets: parse response: %w", err)
	}
	if resp.FailedEntryCount == 0 {
		return nil
	}
	if len(resp.FailedEntries) == 0 {
		return fmt.Errorf("PutTargets: %d target(s) rejected", resp.FailedEntryCount)
	}
	first := resp.FailedEntries[0]
	return fmt.Errorf("PutTargets: target %q rejected (%s): %s", first.TargetID, first.ErrorCode, first.ErrorMessage)
}

func eventRuleNameFromArn(arn string) string {
	if idx := strings.LastIndex(arn, "/"); idx >= 0 {
		return arn[idx+1:]
	}
	return arn
}

func eventRuleIdentityFromArn(arn string) (string, string) {
	const marker = ":rule/"
	idx := strings.Index(arn, marker)
	if idx < 0 {
		return "", eventRuleNameFromArn(arn)
	}
	resource := arn[idx+len(marker):]
	parts := strings.Split(resource, "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", resource
}

func (h *eventsRuleHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Extract name from ARN: arn:aws:events:region:acct:rule/[bus/]name
	eventBusName, name := eventRuleIdentityFromArn(physicalID)
	body := map[string]any{"Name": name}
	if eventBusName != "" {
		body["EventBusName"] = eventBusName
	}
	// EventBridge refuses DeleteRule while the rule still has targets ("Before
	// you can delete the rule, you must remove all targets, using
	// RemoveTargets" — API_DeleteRule), and Force is the managed-rule escape
	// rather than a way around that. Create attached these targets, so Delete
	// takes them off again — which is what real CloudFormation's resource
	// provider does, and why an AWS::Events::Rule with targets deletes cleanly
	// on AWS despite the API rule.
	if err := removeAllEventTargets(ctx, router, rCtx.Region, name, eventBusName); err != nil {
		return err
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSEvents.DeleteRule", body)
	return teardownError("DeleteRule", rec, err)
}

// removeAllEventTargets detaches every target currently on a rule, so the rule
// can be deleted. It removes what ListTargetsByRule reports rather than what
// the template declared: an Update may have changed the set, and a target
// added outside the stack would block the delete just as firmly.
//
// A rule that is already gone reports no targets and needs nothing removed, so
// the teardown stays idempotent the way every other handler's Delete is.
func removeAllEventTargets(ctx context.Context, router http.Handler, region, ruleName, eventBusName string) error {
	listBody := map[string]any{"Rule": ruleName}
	if eventBusName != "" {
		listBody["EventBusName"] = eventBusName
	}
	rec, err := internalJSON(ctx, router, region, "AWSEvents.ListTargetsByRule", listBody)
	if err != nil {
		return teardownError("ListTargetsByRule", rec, err)
	}
	var listed struct {
		Targets []struct {
			ID string `json:"Id"`
		} `json:"Targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		return fmt.Errorf("ListTargetsByRule: parse response: %w", err)
	}
	if len(listed.Targets) == 0 {
		return nil
	}
	ids := make([]string, 0, len(listed.Targets))
	for _, t := range listed.Targets {
		ids = append(ids, t.ID)
	}
	rmBody := map[string]any{"Rule": ruleName, "Ids": ids}
	if eventBusName != "" {
		rmBody["EventBusName"] = eventBusName
	}
	rec, err = internalJSON(ctx, router, region, "AWSEvents.RemoveTargets", rmBody)
	return teardownError("RemoveTargets", rec, err)
}

func (h *eventsRuleHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if n, ok := props["Name"].(string); ok && n != "" {
		tail := physicalID
		if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
			tail = physicalID[idx+1:]
		}
		if tail != n {
			return "", nil, errReplacementRequired
		}
	}
	if newBus, _ := props["EventBusName"].(string); oldProps != nil {
		if oldBus, _ := oldProps["EventBusName"].(string); oldBus != "" && newBus != oldBus {
			return "", nil, errReplacementRequired
		}
	}

	eventBusName, ruleName := eventRuleIdentityFromArn(physicalID)
	body := make(map[string]any)
	if v, _ := props["Name"].(string); v != "" {
		body["Name"] = v
	} else if ruleName != "" {
		body["Name"] = ruleName
	}
	if v, _ := props["EventBusName"].(string); v != "" {
		body["EventBusName"] = v
		eventBusName = v
	} else if eventBusName != "" {
		body["EventBusName"] = eventBusName
	}
	if v, _ := props["State"].(string); v != "" {
		body["State"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if v, ok := props["RoleArn"]; ok {
		body["RoleArn"] = v
	}
	if v, ok := props["EventPattern"].(map[string]any); ok && v != nil {
		j, _ := json.Marshal(v)
		body["EventPattern"] = string(j)
	} else if v, _ := props["EventPattern"].(string); v != "" {
		body["EventPattern"] = v
	}
	if v, _ := props["ScheduleExpression"].(string); v != "" {
		body["ScheduleExpression"] = v
	}

	if _, err := internalJSON(ctx, router, rCtx.Region, "AWSEvents.PutRule", body); err != nil {
		return "", nil, fmt.Errorf("PutRule: %w", err)
	}

	// Diff targets: remove old, add new.
	newTargets, _ := props["Targets"].([]any)
	var oldTargetList []any
	if oldProps != nil {
		oldTargetList, _ = oldProps["Targets"].([]any)
	}

	oldIDs := make(map[string]bool)
	for _, t := range oldTargetList {
		if m, ok := t.(map[string]any); ok {
			if id, _ := m["Id"].(string); id != "" {
				oldIDs[id] = true
			}
		}
	}
	newIDs := make(map[string]bool)
	for _, t := range newTargets {
		if m, ok := t.(map[string]any); ok {
			if id, _ := m["Id"].(string); id != "" {
				newIDs[id] = true
			}
		}
	}

	var toRemoveIDs []string
	for id := range oldIDs {
		if !newIDs[id] {
			toRemoveIDs = append(toRemoveIDs, id)
		}
	}
	if len(toRemoveIDs) > 0 {
		rmBody := map[string]any{"Rule": ruleName, "Ids": toRemoveIDs}
		if eventBusName != "" {
			rmBody["EventBusName"] = eventBusName
		}
		if _, err := internalJSON(ctx, router, rCtx.Region, "AWSEvents.RemoveTargets", rmBody); err != nil {
			return "", nil, fmt.Errorf("RemoveTargets: %w", err)
		}
	}

	var toAdd []any
	for _, t := range newTargets {
		if m, ok := t.(map[string]any); ok {
			if id, _ := m["Id"].(string); id != "" {
				toAdd = append(toAdd, t)
			}
		}
	}
	if len(toAdd) > 0 {
		if err := putEventTargets(ctx, router, rCtx.Region, ruleName, eventBusName, toAdd); err != nil {
			return "", nil, err
		}
	}

	return physicalID, map[string]string{"Arn": physicalID}, nil
}

// ── AWS::KMS::Key ──────────────────────────────────────────────────────────

type kmsKeyHandler struct{}

// kmsKeyPolicyString serializes a CloudFormation KeyPolicy property for the
// KMS Policy parameter. The property is Type: Json, which accepts both an
// object and a JSON string; a string must pass through verbatim because
// marshalling it again would double-encode it into a quoted string that KMS
// policy validation rejects.
func kmsKeyPolicyString(keyPolicy any) (string, error) {
	if s, ok := keyPolicy.(string); ok {
		return s, nil
	}
	policy, err := json.Marshal(keyPolicy)
	if err != nil {
		return "", err
	}
	return string(policy), nil
}

// kmsTagsFromMap renders a merged tag map as the {TagKey, TagValue} shape
// KMS's TagResource/CreateKey Tags parameter uses (distinct from the
// {Key, Value} shape the CloudFormation Tags property itself carries).
func kmsTagsFromMap(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]string{"TagKey": key, "TagValue": tags[key]})
	}
	return out
}

// updateKmsTags reconciles a KMS key's tags to the template's current set via
// TagResource/UntagResource, the same add/remove diff every other tag-aware
// CloudFormation handler here uses.
func updateKmsTags(ctx context.Context, router http.Handler, region, keyID string, tags, prior map[string]string) (bool, error) {
	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	applied := false
	if len(added) > 0 {
		if _, err := internalJSON(ctx, router, region, "TrentService.TagResource", map[string]any{
			"KeyId": keyID,
			"Tags":  kmsTagsFromMap(added),
		}); err != nil {
			return false, fmt.Errorf("TagResource: %w", err)
		}
		applied = true
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, exists := tags[key]; !exists {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	if len(removed) > 0 {
		if _, err := internalJSON(ctx, router, region, "TrentService.UntagResource", map[string]any{
			"KeyId":   keyID,
			"TagKeys": removed,
		}); err != nil {
			return applied, fmt.Errorf("UntagResource: %w", err)
		}
		applied = true
	}
	return applied, nil
}

func (h *kmsKeyHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if v, _ := props["KeySpec"].(string); v != "" {
		body["KeySpec"] = v
	}
	if v, _ := props["KeyUsage"].(string); v != "" {
		body["KeyUsage"] = v
	}
	if keyPolicy, ok := props["KeyPolicy"]; ok {
		policy, err := kmsKeyPolicyString(keyPolicy)
		if err != nil {
			return "", nil, fmt.Errorf("CreateKey: serialize KeyPolicy: %w", err)
		}
		body["Policy"] = policy
	}
	if bypass, ok := props["BypassPolicyLockoutSafetyCheck"]; ok {
		body["BypassPolicyLockoutSafetyCheck"] = asBool(bypass)
	}
	// Origin and MultiRegion are forwarded rather than validated here: the KMS
	// service is the one place that knows what it can and cannot honour
	// (AWS_KMS-origin material only, no replica-key relationship), and it
	// rejects anything else loudly instead of the emulator silently keeping
	// the AWS default.
	if v, _ := props["Origin"].(string); v != "" {
		body["Origin"] = v
	}
	if v, ok := props["MultiRegion"]; ok {
		body["MultiRegion"] = asBool(v)
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = kmsTagsFromMap(tags)
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "TrentService.CreateKey", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateKey: %w", err)
	}

	var resp struct {
		KeyMetadata struct {
			KeyID string `json:"KeyId"`
			Arn   string `json:"Arn"`
		} `json:"KeyMetadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateKey: parse response: %w", err)
	}

	attrs := map[string]string{
		"KeyId": resp.KeyMetadata.KeyID,
		"Arn":   resp.KeyMetadata.Arn,
	}

	// cleanupOnFailure schedules deletion of the key CreateKey already
	// persisted: this Create method cannot return a physical ID on failure
	// for the provisioner to clean up otherwise.
	cleanupOnFailure := func(cause error) (string, map[string]string, error) {
		if _, cleanupErr := internalJSON(ctx, router, rCtx.Region, "TrentService.ScheduleKeyDeletion", map[string]any{
			"KeyId":               resp.KeyMetadata.KeyID,
			"PendingWindowInDays": 7,
		}); cleanupErr != nil {
			return "", nil, fmt.Errorf("%w; ScheduleKeyDeletion cleanup: %v", cause, cleanupErr)
		}
		return "", nil, cause
	}

	if enabled, ok := props["Enabled"]; ok && !asBool(enabled) {
		keyBody := map[string]any{"KeyId": resp.KeyMetadata.KeyID}
		if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.DisableKey", keyBody); err != nil {
			return cleanupOnFailure(fmt.Errorf("DisableKey: %w", err))
		}
	}
	// EnableKeyRotation is dispatched to the real (currently unmodeled)
	// operation rather than silently dropped — this KMS emulation does not
	// track rotation state at all, so a template that turns rotation on
	// deserves a loud failure telling the CDK user that, not a key that
	// quietly reports rotation off forever.
	if rotation, ok := props["EnableKeyRotation"]; ok && asBool(rotation) {
		if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.EnableKeyRotation", map[string]any{"KeyId": resp.KeyMetadata.KeyID}); err != nil {
			return cleanupOnFailure(fmt.Errorf("EnableKeyRotation: %w", err))
		}
	}
	return resp.KeyMetadata.KeyID, attrs, nil
}

// DeleteWithProperties tears down the key, carrying the template's
// PendingWindowInDays through to ScheduleKeyDeletion so a CDK key that asked
// for a short (or long) retention window actually gets it instead of the
// emulator's own compensating-cleanup default.
func (h *kmsKeyHandler) DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	body := map[string]any{"KeyId": physicalID}
	if raw, ok := props["PendingWindowInDays"]; ok {
		days, err := cfnInt64(raw)
		if err != nil {
			return fmt.Errorf("ScheduleKeyDeletion: PendingWindowInDays: %w", err)
		}
		body["PendingWindowInDays"] = days
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "TrentService.ScheduleKeyDeletion", body)
	return teardownError("ScheduleKeyDeletion", rec, err)
}

// Delete satisfies resourceHandler for callers that reach a KMS key without
// its stored properties (invokeDelete always prefers DeleteWithProperties
// above when they are available). It omits PendingWindowInDays, so the KMS
// service applies its own documented default (30 days).
func (h *kmsKeyHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	return h.DeleteWithProperties(ctx, router, cfg, physicalID, nil, rCtx)
}

func (h *kmsKeyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newSpec, _ := props["KeySpec"].(string); newSpec != "" {
			if oldSpec, _ := oldProps["KeySpec"].(string); oldSpec != "" && newSpec != oldSpec {
				return "", nil, errReplacementRequired
			}
		}
		if newUsage, _ := props["KeyUsage"].(string); newUsage != "" {
			if oldUsage, _ := oldProps["KeyUsage"].(string); oldUsage != "" && newUsage != oldUsage {
				return "", nil, errReplacementRequired
			}
		}
		// Origin and MultiRegion are both "Update requires: Replacement" on the
		// real resource, and neither has a KMS API to change them in place.
		newOrigin, _ := props["Origin"].(string)
		oldOrigin, _ := oldProps["Origin"].(string)
		if newOrigin != "" && oldOrigin != "" && newOrigin != oldOrigin {
			return "", nil, errReplacementRequired
		}
		if asBool(props["MultiRegion"]) != asBool(oldProps["MultiRegion"]) {
			return "", nil, errReplacementRequired
		}
	}

	policyChanged, err := cfnPropertyChanged(props, oldProps, "KeyPolicy")
	if err != nil {
		return physicalID, nil, failUpdate(err)
	}
	var previousPolicy string
	policyApplied := false
	if keyPolicy, ok := props["KeyPolicy"]; ok && policyChanged {
		// Real CloudFormation calls PutKeyPolicy only on a policy diff.
		// Re-putting an unchanged caller-locking policy would re-run lockout
		// validation without the bypass that originally created it.
		policy, err := kmsKeyPolicyString(keyPolicy)
		if err != nil {
			return physicalID, nil, failUpdate(fmt.Errorf("PutKeyPolicy: serialize KeyPolicy: %w", err))
		}
		getRec, err := internalJSON(ctx, router, rCtx.Region, "TrentService.GetKeyPolicy", map[string]any{
			"KeyId": physicalID, "PolicyName": "default",
		})
		if err != nil {
			return physicalID, nil, failUpdate(fmt.Errorf("GetKeyPolicy before update: %w", err))
		}
		var current struct {
			Policy string `json:"Policy"`
		}
		if err := json.Unmarshal(getRec.Body.Bytes(), &current); err != nil {
			return physicalID, nil, failUpdate(fmt.Errorf("GetKeyPolicy before update: parse response: %w", err))
		}
		previousPolicy = current.Policy
		body := map[string]any{
			"KeyId":      physicalID,
			"PolicyName": "default",
			"Policy":     policy,
		}
		if bypass, ok := props["BypassPolicyLockoutSafetyCheck"]; ok {
			body["BypassPolicyLockoutSafetyCheck"] = asBool(bypass)
		}
		if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.PutKeyPolicy", body); err != nil {
			return physicalID, nil, failUpdate(fmt.Errorf("PutKeyPolicy: %w", err))
		}
		policyApplied = true
	}

	// restorePolicy compensates an applied policy change when a later
	// sub-operation fails; bypass is forced because the previous policy may
	// exclude the local caller.
	restorePolicy := func(opErr error) (string, map[string]string, error) {
		if policyApplied {
			_, restoreErr := internalJSON(ctx, router, rCtx.Region, "TrentService.PutKeyPolicy", map[string]any{
				"KeyId": physicalID, "PolicyName": "default", "Policy": previousPolicy,
				"BypassPolicyLockoutSafetyCheck": true,
			})
			if restoreErr != nil {
				return physicalID, nil, failDirtyUpdate(errors.Join(opErr,
					fmt.Errorf("restore KMS key policy: %w", restoreErr)))
			}
		}
		return physicalID, nil, failUpdate(opErr)
	}

	descriptionChanged, err := cfnPropertyChanged(props, oldProps, "Description")
	if err != nil {
		return physicalID, nil, failUpdate(err)
	}
	if descriptionChanged {
		// A removed Description restores the documented default (empty).
		description, _ := props["Description"].(string)
		if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.UpdateKeyDescription", map[string]any{
			"KeyId": physicalID, "Description": description,
		}); err != nil {
			return restorePolicy(fmt.Errorf("UpdateKeyDescription: %w", err))
		}
	}

	newEnabledBool := true
	if newEnabled, ok := props["Enabled"]; ok {
		newEnabledBool = asBool(newEnabled)
	}
	oldEnabledBool := true
	if oldEnabled, ok := oldProps["Enabled"]; ok {
		oldEnabledBool = asBool(oldEnabled)
	}
	if newEnabledBool != oldEnabledBool {
		kb := map[string]any{"KeyId": physicalID}
		var transitionErr error
		if newEnabledBool {
			if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.EnableKey", kb); err != nil {
				transitionErr = fmt.Errorf("EnableKey: %w", err)
			}
		} else {
			if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.DisableKey", kb); err != nil {
				transitionErr = fmt.Errorf("DisableKey: %w", err)
			}
		}
		if transitionErr != nil {
			return restorePolicy(transitionErr)
		}
	}

	newRotation := asBool(props["EnableKeyRotation"])
	oldRotation := asBool(oldProps["EnableKeyRotation"])
	if newRotation && !oldRotation {
		// Dispatched to the real (currently unmodeled) operation so a template
		// that turns rotation on gets a loud failure instead of a key that
		// quietly reports rotation off forever — see the matching comment on
		// Create.
		if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.EnableKeyRotation", map[string]any{"KeyId": physicalID}); err != nil {
			return restorePolicy(fmt.Errorf("EnableKeyRotation: %w", err))
		}
	}

	newTags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	oldTags := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(newTags, oldTags) {
		if _, err := updateKmsTags(ctx, router, rCtx.Region, physicalID, newTags, oldTags); err != nil {
			return restorePolicy(err)
		}
	}

	return physicalID, nil, nil
}

// ── AWS::KMS::Alias ────────────────────────────────────────────────────────

type kmsAliasHandler struct{}

func (h *kmsAliasHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	aliasName, _ := props["AliasName"].(string)
	targetKeyID, _ := props["TargetKeyId"].(string)

	body := map[string]any{
		"AliasName":   aliasName,
		"TargetKeyId": targetKeyID,
	}

	if _, err := internalJSON(ctx, router, rCtx.Region, "TrentService.CreateAlias", body); err != nil {
		return "", nil, fmt.Errorf("CreateAlias: %w", err)
	}

	attrs := map[string]string{
		"AliasName":   aliasName,
		"TargetKeyId": targetKeyID,
	}
	return aliasName, attrs, nil
}

func (h *kmsAliasHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"AliasName": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "TrentService.DeleteAlias", body)
	return teardownError("DeleteAlias", rec, err)
}

// ── AWS::Lambda::EventSourceMapping ────────────────────────────────────────

type lambdaEventSourceMappingHandler struct{}

// lambdaESMCreateBody builds the CreateEventSourceMapping request an
// AWS::Lambda::EventSourceMapping template resource dispatches. It is a pure
// function of the resolved properties so that the set of request members this
// resource can forward is derivable by running it — see
// TestLambdaProvisionerForwardsOnlyReviewedGatedMembers, which checks that set
// against Lambda's own 501 gate.
func lambdaESMCreateBody(props map[string]any, stackTags []Tag) map[string]any {
	body := map[string]any{}
	if v, _ := props["EventSourceArn"].(string); v != "" {
		body["EventSourceArn"] = v
	}
	if v, _ := props["FunctionName"].(string); v != "" {
		body["FunctionName"] = v
	}
	if v, ok := props["BatchSize"]; ok {
		body["BatchSize"] = v
	}
	if v, ok := props["Enabled"]; ok {
		body["Enabled"] = v
	}
	if v, _ := props["StartingPosition"].(string); v != "" {
		body["StartingPosition"] = v
	}
	for _, key := range lambdaESMCreateForwardedProperties {
		if v, ok := props[key]; ok {
			body[key] = v
		}
	}
	copyAnyProp(body, props, "KmsKeyArn", "KMSKeyArn")
	if tags := mergeResourceTags(stackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = tags
	}
	return body
}

// lambdaESMCreateForwardedProperties are the template properties copied
// straight through to CreateEventSourceMapping under the same name.
var lambdaESMCreateForwardedProperties = []string{
	"MaximumBatchingWindowInSeconds",
	"FilterCriteria",
	"MaximumRecordAgeInSeconds",
	"MaximumRetryAttempts",
	"TumblingWindowInSeconds",
	"BisectBatchOnFunctionError",
	"DestinationConfig",
	"ScalingConfig",
	"FunctionResponseTypes",
	"ParallelizationFactor",
	"StartingPositionTimestamp",
	"SourceAccessConfigurations",
	"SelfManagedEventSource",
	"Topics",
	"Queues",
	"MetricsConfig",
	"ProvisionedPollerConfig",
	"AmazonManagedKafkaEventSourceConfig",
	"DocumentDBEventSourceConfig",
	"LoggingConfig",
	"SelfManagedKafkaEventSourceConfig",
}

func (h *lambdaEventSourceMappingHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := lambdaESMCreateBody(props, rCtx.StackTags)

	data, _ := json.Marshal(body)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2015-03-31/event-source-mappings/", "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateEventSourceMapping: %w", err)
	}

	var resp struct {
		UUID                  string `json:"UUID"`
		EventSourceMappingArn string `json:"EventSourceMappingArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateEventSourceMapping: parse response: %w", err)
	}

	attrs := map[string]string{
		"Id": resp.UUID,
	}
	if resp.EventSourceMappingArn != "" {
		attrs["EventSourceMappingArn"] = resp.EventSourceMappingArn
	}
	return resp.UUID, attrs, nil
}

func (h *lambdaEventSourceMappingHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	path := fmt.Sprintf("/2015-03-31/event-source-mappings/%s", physicalID)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteEventSourceMapping", rec, err)
}

func (h *lambdaEventSourceMappingHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := validateRequiredPropertyRemovals(props, oldProps, rCtx.LogicalID, "AWS::Lambda::EventSourceMapping", "FunctionName"); err != nil {
		return "", nil, failUpdate(err)
	}
	if !reflect.DeepEqual(props["EventSourceArn"], oldProps["EventSourceArn"]) {
		return "", nil, errReplacementRequired
	}
	for _, key := range []string{"StartingPosition", "StartingPositionTimestamp", "SelfManagedEventSource"} {
		if !reflect.DeepEqual(props[key], oldProps[key]) {
			return "", nil, errReplacementRequired
		}
	}
	tagsChanged := !reflect.DeepEqual(props["Tags"], oldProps["Tags"]) || !reflect.DeepEqual(rCtx.StackTags, rCtx.PreviousStackTags)
	tagsApplied := false
	tagARN := protocol.ARN(rCtx.Region, rCtx.AccountID, "lambda", "event-source-mapping:"+physicalID)
	if tagsChanged {
		var err error
		tagsApplied, err = updateLambdaTags(ctx, router, rCtx.Region, tagARN, rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], oldProps["Tags"])
		if err != nil {
			var compensationErr error
			if tagsApplied {
				_, compensationErr = updateLambdaTags(ctx, router, rCtx.Region, tagARN, rCtx.PreviousStackTags, rCtx.StackTags, oldProps["Tags"], props["Tags"])
			}
			if compensationErr != nil {
				return "", nil, failDirtyUpdate(errors.Join(err, compensationErr))
			}
			return "", nil, failUpdate(err)
		}
	}

	body, haveMutable := lambdaESMUpdateBody(physicalID, props, oldProps)
	if haveMutable {
		data, _ := json.Marshal(body)
		path := fmt.Sprintf("/2015-03-31/event-source-mappings/%s", physicalID)
		if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, path, "application/json", data); err != nil {
			if tagsApplied {
				_, compensationErr := updateLambdaTags(ctx, router, rCtx.Region, tagARN, rCtx.PreviousStackTags, rCtx.StackTags, oldProps["Tags"], props["Tags"])
				updateErr := fmt.Errorf("UpdateEventSourceMapping: %w", err)
				if compensationErr != nil {
					return "", nil, failDirtyUpdate(errors.Join(updateErr, compensationErr))
				}
				return "", nil, failUpdate(updateErr)
			}
			return "", nil, failUpdate(fmt.Errorf("UpdateEventSourceMapping: %w", err))
		}
	}

	return physicalID, map[string]string{
		"Id":                    physicalID,
		"EventSourceMappingArn": protocol.ARN(rCtx.Region, rCtx.AccountID, "lambda", "event-source-mapping:"+physicalID),
	}, nil
}

// lambdaESMUpdateBody builds the UpdateEventSourceMapping request for a changed
// AWS::Lambda::EventSourceMapping, and reports whether anything mutable
// actually changed. Pure, for the same reason as lambdaESMCreateBody.
func lambdaESMUpdateBody(physicalID string, props, oldProps map[string]any) (map[string]any, bool) {
	body := map[string]any{"UUID": physicalID}
	haveMutable := false
	for _, key := range lambdaESMUpdateForwardedProperties {
		if reflect.DeepEqual(props[key], oldProps[key]) {
			continue
		}
		if v, ok := props[key]; ok {
			body[key] = v
			haveMutable = true
			continue
		}
		if empty, ok := lambdaEventSourceMappingClearValue(key, fmt.Sprint(oldProps["EventSourceArn"])); ok {
			body[key] = empty
			haveMutable = true
		}
	}
	if !reflect.DeepEqual(props["KmsKeyArn"], oldProps["KmsKeyArn"]) {
		haveMutable = true
		body["KMSKeyArn"] = ""
		if value, ok := props["KmsKeyArn"]; ok {
			body["KMSKeyArn"] = value
		}
	}
	return body, haveMutable
}

// lambdaESMUpdateForwardedProperties are the mutable template properties copied
// straight through to UpdateEventSourceMapping under the same name.
var lambdaESMUpdateForwardedProperties = []string{
	"BatchSize",
	"Enabled",
	"MaximumBatchingWindowInSeconds",
	"FilterCriteria",
	"MaximumRecordAgeInSeconds",
	"MaximumRetryAttempts",
	"BisectBatchOnFunctionError",
	"DestinationConfig",
	"ScalingConfig",
	"FunctionResponseTypes",
	"ParallelizationFactor",
	"TumblingWindowInSeconds",
	"SourceAccessConfigurations",
	"MetricsConfig",
	"ProvisionedPollerConfig",
	"FunctionName",
	"Topics",
	"Queues",
	"AmazonManagedKafkaEventSourceConfig",
	"DocumentDBEventSourceConfig",
	"LoggingConfig",
	"SelfManagedKafkaEventSourceConfig",
}

func lambdaEventSourceMappingClearValue(property, eventSourceARN string) (any, bool) {
	batchSize := 100
	if strings.Contains(eventSourceARN, ":sqs:") {
		batchSize = 10
	}
	values := map[string]any{
		"BatchSize": batchSize, "Enabled": true, "MaximumBatchingWindowInSeconds": 0,
		"FilterCriteria": map[string]any{}, "MaximumRecordAgeInSeconds": -1, "MaximumRetryAttempts": -1,
		"BisectBatchOnFunctionError": false, "DestinationConfig": map[string]any{}, "ScalingConfig": map[string]any{},
		"FunctionResponseTypes": []any{}, "ParallelizationFactor": 1, "TumblingWindowInSeconds": 0,
		"SourceAccessConfigurations": []any{}, "MetricsConfig": map[string]any{}, "ProvisionedPollerConfig": map[string]any{},
		"Topics": []any{}, "Queues": []any{}, "AmazonManagedKafkaEventSourceConfig": map[string]any{},
		"DocumentDBEventSourceConfig": map[string]any{}, "LoggingConfig": map[string]any{},
		"SelfManagedKafkaEventSourceConfig": map[string]any{},
	}
	value, ok := values[property]
	return value, ok
}

// ── AWS::Lambda::LayerVersion ──────────────────────────────────────────────

type lambdaLayerVersionHandler struct{}

func (h *lambdaLayerVersionHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	layerName, _ := props["LayerName"].(string)
	if layerName == "" {
		layerName = rCtx.generatedNameWithin(maxNameLenLambdaLayer)
	}

	body := map[string]any{}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if v, ok := props["Content"]; ok {
		body["Content"] = v
	}
	if v, ok := props["CompatibleRuntimes"]; ok {
		body["CompatibleRuntimes"] = v
	}

	data, _ := json.Marshal(body)
	path := fmt.Sprintf("/2018-10-31/layers/%s/versions", layerName)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("PublishLayerVersion: %w", err)
	}

	var resp struct {
		LayerVersionArn string `json:"LayerVersionArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("PublishLayerVersion: parse response: %w", err)
	}

	arn := resp.LayerVersionArn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:lambda:%s:%s:layer:%s:1", rCtx.Region, rCtx.AccountID, layerName)
	}

	attrs := map[string]string{
		"LayerVersionArn": arn,
	}
	return arn, attrs, nil
}

func (h *lambdaLayerVersionHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Layer versions are immutable — no delete in most emulators.
	return nil
}

// ── AWS::Lambda::CodeSigningConfig ─────────────────────────────────────────
//
// What CDK's CodeSigningConfig construct synthesises. The configuration has to
// exist as a real resource before a Function can reference it, because
// CreateFunction rejects an ARN that names no configuration.

type lambdaCodeSigningConfigHandler struct{}

func (h *lambdaCodeSigningConfigHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if v, ok := props["AllowedPublishers"]; ok {
		body["AllowedPublishers"] = v
	}
	if v, ok := props["CodeSigningPolicies"]; ok {
		body["CodeSigningPolicies"] = v
	}

	data, _ := json.Marshal(body)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2020-04-22/code-signing-configs/", "application/json", data)
	if err != nil {
		return "", nil, fmt.Errorf("CreateCodeSigningConfig: %w", err)
	}

	var resp struct {
		CodeSigningConfig struct {
			CodeSigningConfigID  string `json:"CodeSigningConfigId"`
			CodeSigningConfigArn string `json:"CodeSigningConfigArn"`
		} `json:"CodeSigningConfig"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateCodeSigningConfig: parse response: %w", err)
	}
	arn := resp.CodeSigningConfig.CodeSigningConfigArn
	if arn == "" {
		return "", nil, fmt.Errorf("CreateCodeSigningConfig: no CodeSigningConfigArn in response")
	}

	// Ref is the ARN; both documented GetAtt targets are returned.
	attrs := map[string]string{
		"CodeSigningConfigArn": arn,
		"CodeSigningConfigId":  resp.CodeSigningConfig.CodeSigningConfigID,
	}
	return arn, attrs, nil
}

func (h *lambdaCodeSigningConfigHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	path := "/2020-04-22/code-signing-configs/" + physicalID
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteCodeSigningConfig", rec, err)
}

// ── AWS::StepFunctions::StateMachine ───────────────────────────────────────

type sfnStateMachineHandler struct{}

// sfnSubstitutionToken matches a DefinitionSubstitutions placeholder in an
// ASL definition: a whole ${identifier} token of letters, digits and
// underscore. CloudFormation does not support the Fn::Sub "${!name}" escape
// here — DefinitionSubstitutions is a distinct, StepFunctions-specific
// mechanism the state machine resource applies to an already-fully-resolved
// DefinitionString, not the template-level Fn::Sub intrinsic.
var sfnSubstitutionToken = regexp.MustCompile(`\$\{[A-Za-z0-9_]+\}`)

// applySFNDefinitionSubstitutions replaces every ${key} placeholder in an ASL
// definition with its value from the resource's DefinitionSubstitutions map,
// matching real AWS: substitution happens once, before the definition is
// stored, so the interpreter (and DescribeStateMachine) never sees the
// placeholder syntax for a key that was supplied.
//
// A placeholder with no matching key is left verbatim rather than failing the
// deploy. CloudFormation does not validate DefinitionSubstitutions
// completeness against the definition at deploy time — an unresolved
// ${Foo} is still valid JSON string content, so the create/update succeeds
// the same way a real CDK deploy with a missing substitution key would; the
// state machine simply carries a literal placeholder until it is next
// updated with the missing key or executed against a task that dereferences
// it. This is a deliberate departure from stricter emulators (e.g.
// LocalStack) that reject an unmatched key outright.
func applySFNDefinitionSubstitutions(definition string, rawSubstitutions any) string {
	subs, ok := rawSubstitutions.(map[string]any)
	if !ok || len(subs) == 0 {
		return definition
	}
	return sfnSubstitutionToken.ReplaceAllStringFunc(definition, func(token string) string {
		key := token[2 : len(token)-1] // strip ${ and }
		val, ok := subs[key]
		if !ok {
			return token
		}
		return fmt.Sprint(val)
	})
}

// sfnDefinitionFromProps resolves DefinitionString, Definition or
// DefinitionS3Location into the ASL text CreateStateMachine/UpdateStateMachine
// expect, with DefinitionSubstitutions already applied. Returns "", false when
// none of them is set, and an error when DefinitionS3Location cannot be read.
func sfnDefinitionFromProps(ctx context.Context, router http.Handler, region string, props map[string]any) (string, bool, error) {
	var definition string
	switch {
	case props["DefinitionString"] != nil:
		v, _ := props["DefinitionString"].(string)
		if v == "" {
			return "", false, nil
		}
		definition = v
	case props["Definition"] != nil:
		v, ok := props["Definition"].(map[string]any)
		if !ok {
			return "", false, nil
		}
		j, _ := json.Marshal(v)
		definition = string(j)
	case props["DefinitionS3Location"] != nil:
		fetched, err := sfnDefinitionS3Location(ctx, router, region, props["DefinitionS3Location"])
		if err != nil {
			return "", false, err
		}
		definition = fetched
	default:
		return "", false, nil
	}
	return applySFNDefinitionSubstitutions(definition, props["DefinitionSubstitutions"]), true, nil
}

// sfnTagsWire converts a plain tag map into the lower-camel {key,value} shape
// AWS JSON 1.0's TagResource/UntagResource expect on the wire.
func sfnTagsWire(tags map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for k, v := range tags {
		out = append(out, map[string]string{"key": k, "value": v})
	}
	return out
}

func sfnTagResource(ctx context.Context, router http.Handler, region, arn string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	if _, err := internalJSON(ctx, router, region, "AWSStepFunctions.TagResource", map[string]any{
		"resourceArn": arn,
		"tags":        sfnTagsWire(tags),
	}); err != nil {
		return fmt.Errorf("stepfunctions TagResource: %w", err)
	}
	return nil
}

func sfnUntagResource(ctx context.Context, router http.Handler, region, arn string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if _, err := internalJSON(ctx, router, region, "AWSStepFunctions.UntagResource", map[string]any{
		"resourceArn": arn,
		"tagKeys":     keys,
	}); err != nil {
		return fmt.Errorf("stepfunctions UntagResource: %w", err)
	}
	return nil
}

// sfnReconcileTags diffs desired against previous and applies only the
// change, mirroring updateLambdaTags' and the SSM parameter handler's
// add/remove split.
func sfnReconcileTags(ctx context.Context, router http.Handler, region, arn string, tags, prior map[string]string) error {
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
	if err := sfnTagResource(ctx, router, region, arn, added); err != nil {
		return err
	}
	return sfnUntagResource(ctx, router, region, arn, removed)
}

func (h *sfnStateMachineHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	body := map[string]any{}
	// StateMachineName is optional on the resource and CDK's `StateMachine`
	// leaves it out unless asked; CreateStateMachine requires it.
	if v, _ := props["StateMachineName"].(string); v != "" {
		body["name"] = v
	} else {
		body["name"] = rCtx.generatedNameWithin(maxNameLenSFN)
	}
	definition, ok, err := sfnDefinitionFromProps(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	if ok {
		body["definition"] = definition
	}
	if v, _ := props["RoleArn"].(string); v != "" {
		body["roleArn"] = v
	}
	if v, _ := props["StateMachineType"].(string); v != "" {
		body["type"] = v
	}
	if v, ok := props["LoggingConfiguration"]; ok {
		body["loggingConfiguration"] = v
	}
	if v, ok := props["TracingConfiguration"]; ok {
		body["tracingConfiguration"] = v
	}
	noteUnconsumedProperties(ctx, "AWS::StepFunctions::StateMachine", props,
		"StateMachineName", "DefinitionString", "Definition", "DefinitionS3Location", "DefinitionSubstitutions",
		"RoleArn", "StateMachineType", "LoggingConfiguration", "TracingConfiguration", "Tags")

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.CreateStateMachine", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateStateMachine: %w", err)
	}

	var resp struct {
		StateMachineArn string `json:"stateMachineArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateStateMachine: parse response: %w", err)
	}

	arn := resp.StateMachineArn
	name := ""
	if idx := strings.LastIndex(arn, ":"); idx >= 0 {
		name = arn[idx+1:]
	}

	// CreateStateMachine's own request has no tags parameter on this wire
	// path (see the service handler); apply them the way an update must
	// anyway, via TagResource, so Create and Update share one mechanism.
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		if err := sfnTagResource(ctx, router, rCtx.Region, arn, tags); err != nil {
			return "", nil, err
		}
	}

	revision, err := sfnRevisionAttr(ctx, router, rCtx.Region, arn)
	if err != nil {
		return arn, nil, err
	}
	attrs := map[string]string{
		"Arn":                    arn,
		"Name":                   name,
		"StateMachineRevisionId": revision,
	}
	return arn, attrs, nil
}

func (h *sfnStateMachineHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"stateMachineArn": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.DeleteStateMachine", body)
	return teardownError("DeleteStateMachine", rec, err)
}

func (h *sfnStateMachineHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if n, ok := props["StateMachineName"].(string); ok && n != "" {
		tail := physicalID
		if i := strings.LastIndex(physicalID, ":"); i >= 0 {
			tail = physicalID[i+1:]
		}
		if tail != n {
			return "", nil, errReplacementRequired
		}
	}
	// StateMachineType cannot be changed in place: "You cannot update the
	// type of a state machine once it has been created" (AWS docs), and the
	// CloudFormation resource reference marks it Replacement, unlike every
	// other property here (Update requires: No interruption).
	if t, ok := props["StateMachineType"].(string); ok && t != "" {
		oldType, _ := oldProps["StateMachineType"].(string)
		if oldType == "" {
			oldType = "STANDARD"
		}
		if t != oldType {
			return "", nil, errReplacementRequired
		}
	}

	body := map[string]any{"stateMachineArn": physicalID}
	haveMutable := false
	definition, ok, err := sfnDefinitionFromProps(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	if ok {
		body["definition"] = definition
		haveMutable = true
	}
	if v, _ := props["RoleArn"].(string); v != "" {
		body["roleArn"] = v
		haveMutable = true
	}
	if v, ok := props["LoggingConfiguration"]; ok {
		body["loggingConfiguration"] = v
		haveMutable = true
	}
	if v, ok := props["TracingConfiguration"]; ok {
		body["tracingConfiguration"] = v
		haveMutable = true
	}
	if haveMutable {
		// A rejected or partially applied UpdateStateMachine must fail the
		// resource in place, not fall through to the provisioner's default
		// replace-on-error path: AWS never replaces a state machine over a
		// failed update, and doing so here would mint a new ARN out from
		// under anything already referencing the old one.
		if _, err := internalJSON(ctx, router, rCtx.Region, "AWSStepFunctions.UpdateStateMachine", body); err != nil {
			return "", nil, failUpdate(fmt.Errorf("UpdateStateMachine: %w", err))
		}
	}

	tags := mergeResourceTags(rCtx.StackTags, props["Tags"])
	prior := mergeResourceTags(rCtx.PreviousStackTags, oldProps["Tags"])
	if !reflect.DeepEqual(tags, prior) {
		if err := sfnReconcileTags(ctx, router, rCtx.Region, physicalID, tags, prior); err != nil {
			return "", nil, failUpdate(fmt.Errorf("stepfunctions tags: %w", err))
		}
	}

	// The attributes replace the create-time set, so all three are returned;
	// StateMachineRevisionId moves on whenever the update changed the
	// definition, role, logging or tracing configuration.
	revision, err := sfnRevisionAttr(ctx, router, rCtx.Region, physicalID)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	return physicalID, map[string]string{
		"Arn":                    physicalID,
		"Name":                   physicalID[strings.LastIndex(physicalID, ":")+1:],
		"StateMachineRevisionId": revision,
	}, nil
}

// ── AWS::S3::BucketPolicy ──────────────────────────────────────────────────

type s3BucketPolicyHandler struct{}

func (h *s3BucketPolicyHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	bucket, _ := props["Bucket"].(string)
	if bucket == "" {
		return "", nil, fmt.Errorf("BucketPolicy: Bucket is required")
	}

	policyJSON := policyDocumentJSON(props["PolicyDocument"])

	path := "/" + bucket + "?policy"
	_, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, path, "application/json", policyJSON)
	if err != nil {
		return "", nil, fmt.Errorf("PutBucketPolicy: %w", err)
	}

	return bucket, nil, nil
}

func (h *s3BucketPolicyHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	path := "/" + physicalID + "?policy"
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteBucketPolicy", rec, err)
}

func (h *s3BucketPolicyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, _ map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	bucket, _ := props["Bucket"].(string)
	if bucket != "" && bucket != physicalID {
		return "", nil, errReplacementRequired
	}

	policyJSON := policyDocumentJSON(props["PolicyDocument"])
	path := "/" + physicalID + "?policy"
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPut, path, "application/json", policyJSON); err != nil {
		return "", nil, fmt.Errorf("PutBucketPolicy: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::Logs::LogStream ───────────────────────────────────────────────────

type logsLogStreamHandler struct{}

func (h *logsLogStreamHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	logGroupName, _ := props["LogGroupName"].(string)
	logStreamName, _ := props["LogStreamName"].(string)
	if logStreamName == "" {
		logStreamName = rCtx.generatedName()
	}

	body := map[string]any{
		"logGroupName":  logGroupName,
		"logStreamName": logStreamName,
	}

	_, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.CreateLogStream", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateLogStream: %w", err)
	}

	attrs := map[string]string{
		"LogStreamName": logStreamName,
		"LogGroupName":  logGroupName,
	}
	return logStreamName, attrs, nil
}

func (h *logsLogStreamHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Log streams are cleaned up with log group deletion.
	return nil
}

// ── AWS::Logs::MetricFilter ────────────────────────────────────────────────
//
// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-logs-metricfilter.html
//
// Ref returns the filter name. FilterName and LogGroupName are "Update
// requires: Replacement"; every other property applies in place, which for
// this resource is one more PutMetricFilter, since the service's put is a
// create-or-replace. The handler translates shapes only — MetricTransformations
// carries PascalCase members and a `[{Key, Value}]` Dimensions list where the
// API takes lowerCamel members and a map — and leaves every rule about what a
// transformation may say to the Logs service.

type logsMetricFilterHandler struct{}

// logsMetricFilterConsumed is every property the handler acts on. Anything
// else the template carries — ApplyOnTransformedLogs, EmitSystemFieldDimensions,
// FieldSelectionCriteria, all of which presuppose log transformers or
// centralised logging Overcast does not model — is reported as an emulation
// limitation on the resource rather than dropped in silence.
var logsMetricFilterConsumed = []string{"FilterName", "FilterPattern", "LogGroupName", "MetricTransformations"}

func (h *logsMetricFilterHandler) Create(ctx context.Context, router http.Handler, _ *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["FilterName"].(string)
	if name == "" {
		name = rCtx.generatedName()
	}
	body, err := logsMetricFilterBody(name, props)
	if err != nil {
		return "", nil, err
	}
	noteUnconsumedProperties(ctx, "AWS::Logs::MetricFilter", props, logsMetricFilterConsumed...)
	if _, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.PutMetricFilter", body); err != nil {
		return "", nil, fmt.Errorf("logs PutMetricFilter: %w", err)
	}
	return name, nil, nil
}

func (h *logsMetricFilterHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if n, ok := props["FilterName"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	groupChanged, err := cfnPropertyChanged(props, oldProps, "LogGroupName")
	if err != nil {
		return "", nil, failUpdate(err)
	}
	if groupChanged {
		return "", nil, errReplacementRequired
	}
	body, err := logsMetricFilterBody(physicalID, props)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	// A rejected replacement definition must fail the resource in place: the
	// old filter is still there, exactly as it was, and replacing it would
	// not make the new definition any more valid.
	if _, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.PutMetricFilter", body); err != nil {
		return "", nil, failUpdate(fmt.Errorf("logs PutMetricFilter: %w", err))
	}
	return physicalID, nil, nil
}

// Delete cannot act alone: the physical ID is the filter name, and
// DeleteMetricFilter also needs the log group, which only the stored
// properties record — see DeleteWithProperties, which every delete path
// prefers (invokeDelete).
func (h *logsMetricFilterHandler) Delete(_ context.Context, _ http.Handler, _ *config.Config, physicalID string, _ *resolveContext) error {
	return fmt.Errorf("logs DeleteMetricFilter %s: the log group is only known from the resource's properties", physicalID)
}

// DeleteWithProperties removes the filter, tolerating one already gone — with
// its log group, which deletes its filters, or by hand.
func (h *logsMetricFilterHandler) DeleteWithProperties(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	logGroupName, _ := props["LogGroupName"].(string)
	rec, err := internalJSON(ctx, router, rCtx.Region, "Logs_20140328.DeleteMetricFilter", map[string]any{
		"logGroupName": logGroupName,
		"filterName":   physicalID,
	})
	return teardownError("DeleteMetricFilter", rec, err)
}

// logsMetricFilterBody translates the template's properties into a
// PutMetricFilter request. FilterPattern is "Required: Yes" in the resource
// reference, so a template without it is refused here as AWS refuses it —
// forwarding the empty pattern would deploy a match-everything filter that
// the same template fails to create on AWS. An explicit "" is a valid
// "match everything" and is forwarded as such.
func logsMetricFilterBody(name string, props map[string]any) (map[string]any, error) {
	logGroupName, _ := props["LogGroupName"].(string)
	rawPattern, ok := props["FilterPattern"]
	if !ok || rawPattern == nil {
		return nil, fmt.Errorf("Logs::MetricFilter FilterPattern is required")
	}
	pattern, _ := rawPattern.(string)
	transformations, err := logsMetricTransformations(props["MetricTransformations"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"logGroupName":          logGroupName,
		"filterName":            name,
		"filterPattern":         pattern,
		"metricTransformations": transformations,
	}, nil
}

// logsMetricTransformations reshapes the MetricTransformations list: member
// names to lowerCamel, DefaultValue coerced the way CloudFormation coerces a
// String-typed Ref, and the `[{Key, Value}]` Dimensions list folded into the
// map the API models. Whether the result is acceptable is the service's call.
func logsMetricTransformations(raw any) ([]map[string]any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("Logs::MetricFilter MetricTransformations must be an array")
	}
	out := make([]map[string]any, 0, len(items))
	for i, item := range items {
		t, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Logs::MetricFilter MetricTransformations[%d] must be an object", i)
		}
		body := map[string]any{}
		forwardProperties(t, body, "MetricName", "MetricNamespace", "MetricValue", "Unit")
		if v, ok := t["DefaultValue"]; ok && v != nil {
			f, err := cfnFloat64(v)
			if err != nil {
				return nil, fmt.Errorf("Logs::MetricFilter MetricTransformations[%d].DefaultValue: %w", i, err)
			}
			body["defaultValue"] = f
		}
		if v, ok := t["Dimensions"]; ok && v != nil {
			dims, err := logsMetricFilterDimensions(v)
			if err != nil {
				return nil, fmt.Errorf("Logs::MetricFilter MetricTransformations[%d].%w", i, err)
			}
			body["dimensions"] = dims
		}
		out = append(out, body)
	}
	return out, nil
}

// logsMetricFilterDimensions folds the template's `[{Key, Value}]` list into
// the API's map. A duplicate key is refused: the template said two things and
// picking one would not be what it asked for.
func logsMetricFilterDimensions(raw any) (map[string]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("Dimensions must be an array")
	}
	dims := make(map[string]string, len(items))
	for i, item := range items {
		d, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Dimensions[%d] must be an object", i)
		}
		key, _ := d["Key"].(string)
		value, _ := d["Value"].(string)
		if key == "" || value == "" {
			return nil, fmt.Errorf("Dimensions[%d] must carry Key and Value", i)
		}
		if _, duplicate := dims[key]; duplicate {
			return nil, fmt.Errorf("Dimensions contains duplicate key %q", key)
		}
		dims[key] = value
	}
	return dims, nil
}

// ── Helper: extract XML tag value ──────────────────────────────────────────

// extractXMLTag does a simple extraction of a tag value from raw XML.
// This avoids declaring full XML structs for each IAM response.
func extractXMLTag(body, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	i := strings.Index(body, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(body[i:], close)
	if j < 0 {
		return ""
	}
	return body[i : i+j]
}
