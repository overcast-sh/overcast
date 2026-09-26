package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

func IAM(c *clients.Clients) ServiceGroup {
	g := &iamGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"iam-policies:CreatePolicy":                        g.CreatePolicy,
			"iam-policies:CreatePolicyMalformedDocument":       g.CreatePolicyMalformedDocument,
			"iam-policies:GetPolicy":                           g.GetPolicy,
			"iam-policies:GetPolicyReturnsTags":                g.GetPolicyReturnsTags,
			"iam-policies:ListPolicies":                        g.ListPolicies,
			"iam-policies:GetPolicyAttachmentCountAfterAttach": g.GetPolicyAttachmentCountAfterAttach,
			"iam-policies:GetPolicyAttachmentCountAfterDetach": g.GetPolicyAttachmentCountAfterDetach,
			"iam-policies:DeletePolicy":                        g.DeletePolicy,
			"iam-groups:CreateGroup":                           g.CreateGroup,
			"iam-groups:AddUserToGroup":                        g.AddUserToGroup,
			"iam-groups:ListGroupsForUser":                     g.ListGroupsForUser,
			"iam-groups:RemoveUserFromGroup":                   g.RemoveUserFromGroup,
			"iam-groups:GetGroup":                              g.GetGroup,
			"iam-groups:DeleteGroup":                           g.DeleteGroup,

			"iam-simulate:SimulateCustomPolicyAllowed":         g.SimulateCustomPolicyAllowed,
			"iam-simulate:SimulateCustomPolicyImplicitDeny":    g.SimulateCustomPolicyImplicitDeny,
			"iam-simulate:SimulateCustomPolicyExplicitDeny":    g.SimulateCustomPolicyExplicitDeny,
			"iam-simulate:SimulatePrincipalPolicyAllowed":      g.SimulatePrincipalPolicyAllowed,
			"iam-simulate:SimulatePrincipalPolicyImplicitDeny": g.SimulatePrincipalPolicyImplicitDeny,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"iam-policies": g.setupPolicies,
			"iam-groups":   g.setupGroups,
			"iam-simulate": g.setupSimulate,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"iam-policies": g.teardownPolicies,
			"iam-groups":   g.teardownGroups,
			"iam-simulate": g.teardownSimulate,
		},
	}
}

type iamGroup struct{ c *clients.Clients }

func (g *iamGroup) cl() *iam.Client { return g.c.IAM() }

func iamAssumePolicy() string {
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect":    "Allow",
				"Principal": map[string]interface{}{"Service": "lambda.amazonaws.com"},
				"Action":    "sts:AssumeRole",
			},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// iamMalformedPolicy is a document AWS refuses with MalformedPolicyDocument:
// "Statements must include either an Action or NotAction element" (IAM User
// Guide, reference_policies_elements_action.html). Every writer that takes a
// document names that error (IAM API Reference, API_CreatePolicy.html and
// API_CreateRole.html, Errors).
const iamMalformedPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Resource":"*"}]}`

// iamResourceTags is the tag set the policy fixture is created with, and the
// one GetPolicy must hand back on the resource itself.
func iamResourceTags() []iamtypes.Tag {
	return []iamtypes.Tag{
		{Key: aws.String("owner"), Value: aws.String("compat")},
		{Key: aws.String("stage"), Value: aws.String("dev")},
	}
}

// assertMalformedPolicyDocument checks both halves of the error contract: the
// code AWS's model names, and the 400 the Query protocol binds it to.
func assertMalformedPolicyDocument(op string, err error) error {
	if err == nil {
		return fmt.Errorf("%s: expected MalformedPolicyDocument, got success", op)
	}
	var malformed *iamtypes.MalformedPolicyDocumentException
	if !errors.As(err, &malformed) {
		return fmt.Errorf("%s: expected MalformedPolicyDocument, got %v", op, err)
	}
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) && resp.HTTPStatusCode() != 400 {
		return fmt.Errorf("%s: expected HTTP 400 for MalformedPolicyDocument, got %d", op, resp.HTTPStatusCode())
	}
	return nil
}

// assertResourceTags checks the two fixture tags came back on the resource.
func assertResourceTags(op string, tags []iamtypes.Tag) error {
	got := map[string]string{}
	for _, tag := range tags {
		got[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	for _, want := range iamResourceTags() {
		key, value := aws.ToString(want.Key), aws.ToString(want.Value)
		if got[key] != value {
			return fmt.Errorf("%s: tag %s = %q, want %q (tags: %v)", op, key, got[key], value, got)
		}
	}
	return nil
}

// ── iam-policies ──────────────────────────────────────────────────────────────

func iamPolicyDoc() string {
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{"Effect": "Allow", "Action": []string{"s3:GetObject"}, "Resource": "*"},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func (g *iamGroup) setupPolicies(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-pol-%s", t.RunID)
	resp, err := g.cl().CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(iamPolicyDoc()),
		Tags:           iamResourceTags(),
	})
	if err != nil {
		return err
	}
	t.Set("iam_policy_arn", aws.ToString(resp.Policy.Arn))
	t.Set("iam_policy_name", name)

	// The counter tests attach this group's own policy to a role of its own, so
	// AttachmentCount moves for a customer managed policy rather than for the
	// AWS managed one iam-roles attaches.
	roleName := fmt.Sprintf("oc-polrole-%s", t.RunID)
	if _, err := g.cl().CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(roleName), AssumeRolePolicyDocument: aws.String(iamAssumePolicy()),
	}); err != nil {
		return err
	}
	t.Set("iam_policy_role", roleName)
	return nil
}

func (g *iamGroup) teardownPolicies(ctx context.Context, t *harness.TestContext) error {
	arn := t.GetString("iam_policy_arn")
	if arn == "" {
		return nil
	}
	// detach from all roles first
	if role := t.GetString("iam_policy_role"); role != "" {
		g.cl().DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName: aws.String(role), PolicyArn: aws.String(arn),
		}) //nolint:errcheck
		g.cl().DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)}) //nolint:errcheck
	}
	g.cl().DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)}) //nolint:errcheck
	return nil
}

func (g *iamGroup) CreatePolicy(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-cp-%s", t.RunID)
	resp, err := g.cl().CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(iamPolicyDoc()),
	})
	if err == nil {
		g.cl().DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: resp.Policy.Arn}) //nolint:errcheck
	}
	return err
}

func (g *iamGroup) GetPolicy(ctx context.Context, t *harness.TestContext) error {
	arn := t.GetString("iam_policy_arn")
	resp, err := g.cl().GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
	if err != nil {
		return err
	}
	if resp.Policy == nil || aws.ToString(resp.Policy.Arn) != arn {
		return fmt.Errorf("GetPolicy: wrong ARN %v", resp.Policy)
	}
	return nil
}

func (g *iamGroup) ListPolicies(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().ListPolicies(ctx, &iam.ListPoliciesInput{Scope: "Local"})
	return err
}

// CreatePolicyMalformedDocument pins the same refusal on the identity-policy
// writer.
func (g *iamGroup) CreatePolicyMalformedDocument(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-cpmd-%s", t.RunID)
	resp, err := g.cl().CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(iamMalformedPolicy),
	})
	if err == nil && resp.Policy != nil {
		g.cl().DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: resp.Policy.Arn}) //nolint:errcheck
	}
	return assertMalformedPolicyDocument("CreatePolicyMalformedDocument", err)
}

// GetPolicyReturnsTags pins Tags on the policy resource (API_Policy.html).
func (g *iamGroup) GetPolicyReturnsTags(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(t.GetString("iam_policy_arn"))})
	if err != nil {
		return err
	}
	if resp.Policy == nil {
		return fmt.Errorf("GetPolicyReturnsTags: missing Policy")
	}
	return assertResourceTags("GetPolicyReturnsTags", resp.Policy.Tags)
}

// policyAttachmentCount reads AttachmentCount back through GetPolicy.
func (g *iamGroup) policyAttachmentCount(ctx context.Context, t *harness.TestContext, op string) (int32, error) {
	resp, err := g.cl().GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(t.GetString("iam_policy_arn"))})
	if err != nil {
		return 0, err
	}
	if resp.Policy == nil || resp.Policy.AttachmentCount == nil {
		return 0, fmt.Errorf("%s: GetPolicy returned no AttachmentCount", op)
	}
	return *resp.Policy.AttachmentCount, nil
}

// GetPolicyAttachmentCountAfterAttach pins AttachmentCount moving 0 to 1.
// "The number of entities (users, groups, and roles) that the policy is
// attached to" (IAM API Reference, API_Policy.html) is what a cleanup script
// reads before deleting a policy, so a stuck 0 deletes something in use.
func (g *iamGroup) GetPolicyAttachmentCountAfterAttach(ctx context.Context, t *harness.TestContext) error {
	const op = "GetPolicyAttachmentCountAfterAttach"
	before, err := g.policyAttachmentCount(ctx, t, op)
	if err != nil {
		return err
	}
	if before != 0 {
		return fmt.Errorf("%s: AttachmentCount = %d before the attach, want 0", op, before)
	}
	role := t.GetString("iam_policy_role")
	if role == "" {
		return fmt.Errorf("%s: no role from setup", op)
	}
	if _, err := g.cl().AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName: aws.String(role), PolicyArn: aws.String(t.GetString("iam_policy_arn")),
	}); err != nil {
		return err
	}
	after, err := g.policyAttachmentCount(ctx, t, op)
	if err != nil {
		return err
	}
	if after != 1 {
		return fmt.Errorf("%s: AttachmentCount = %d after attaching to one role, want 1", op, after)
	}
	return nil
}

// GetPolicyAttachmentCountAfterDetach pins the counter moving back to 0, which
// a counter incremented but never decremented would fail.
func (g *iamGroup) GetPolicyAttachmentCountAfterDetach(ctx context.Context, t *harness.TestContext) error {
	const op = "GetPolicyAttachmentCountAfterDetach"
	role := t.GetString("iam_policy_role")
	if role == "" {
		return fmt.Errorf("%s: no role from setup", op)
	}
	if _, err := g.cl().DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
		RoleName: aws.String(role), PolicyArn: aws.String(t.GetString("iam_policy_arn")),
	}); err != nil {
		return err
	}
	after, err := g.policyAttachmentCount(ctx, t, op)
	if err != nil {
		return err
	}
	if after != 0 {
		return fmt.Errorf("%s: AttachmentCount = %d after the detach, want 0", op, after)
	}
	return nil
}

func (g *iamGroup) DeletePolicy(ctx context.Context, t *harness.TestContext) error {
	arn := t.GetString("iam_policy_arn")
	if arn == "" {
		return fmt.Errorf("DeletePolicy: no policy ARN")
	}
	_, err := g.cl().DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)})
	return err
}

// ── iam-groups ─────────────────────────────────────────────────────────────────

func (g *iamGroup) setupGroups(ctx context.Context, t *harness.TestContext) error {
	groupName := fmt.Sprintf("oc-grp-%s", t.RunID)
	if _, err := g.cl().CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(groupName)}); err != nil {
		return err
	}
	userName := fmt.Sprintf("oc-gu-%s", t.RunID)
	if _, err := g.cl().CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(userName)}); err != nil {
		return err
	}
	t.Set("iam_grp", groupName)
	t.Set("iam_grp_user", userName)
	return nil
}

func (g *iamGroup) teardownGroups(ctx context.Context, t *harness.TestContext) error {
	user := t.GetString("iam_grp_user")
	group := t.GetString("iam_grp")
	if user != "" && group != "" {
		g.cl().RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{
			GroupName: aws.String(group), UserName: aws.String(user),
		}) //nolint:errcheck
		g.cl().DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) //nolint:errcheck
	}
	if group != "" {
		g.cl().DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(group)}) //nolint:errcheck
	}
	return nil
}

func (g *iamGroup) CreateGroup(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-cg-%s", t.RunID)
	_, err := g.cl().CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(name)})
	if err == nil {
		g.cl().DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(name)}) //nolint:errcheck
	}
	return err
}

func (g *iamGroup) ListGroups(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().ListGroups(ctx, &iam.ListGroupsInput{})
	return err
}

func (g *iamGroup) AddUserToGroup(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().AddUserToGroup(ctx, &iam.AddUserToGroupInput{
		GroupName: aws.String(t.GetString("iam_grp")),
		UserName:  aws.String(t.GetString("iam_grp_user")),
	})
	return err
}

func (g *iamGroup) ListGroupsForUser(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().ListGroupsForUser(ctx, &iam.ListGroupsForUserInput{
		UserName: aws.String(t.GetString("iam_grp_user")),
	})
	if err != nil {
		return err
	}
	grpName := t.GetString("iam_grp")
	for _, grp := range resp.Groups {
		if aws.ToString(grp.GroupName) == grpName {
			return nil
		}
	}
	return fmt.Errorf("ListGroupsForUser: group %s not found", grpName)
}

func (g *iamGroup) RemoveUserFromGroup(ctx context.Context, t *harness.TestContext) error {
	_, err := g.cl().RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{
		GroupName: aws.String(t.GetString("iam_grp")),
		UserName:  aws.String(t.GetString("iam_grp_user")),
	})
	return err
}

func (g *iamGroup) GetGroup(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().GetGroup(ctx, &iam.GetGroupInput{
		GroupName: aws.String(t.GetString("iam_grp")),
	})
	if err != nil {
		return err
	}
	if resp.Group == nil || aws.ToString(resp.Group.GroupName) != t.GetString("iam_grp") {
		return fmt.Errorf("GetGroup: name mismatch")
	}
	return nil
}

func (g *iamGroup) DeleteGroup(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-dg-%s", t.RunID)
	g.cl().CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(name)}) //nolint:errcheck
	_, err := g.cl().DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(name)})
	return err
}

// ── iam-simulate ───────────────────────────────────────────────────────────────

// iamSimPolicy is the identity policy the simulate group evaluates: read access
// to one run-scoped bucket prefix and nothing else.
func iamSimPolicy(runID string) string {
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect":   "Allow",
				"Action":   "s3:GetObject",
				"Resource": fmt.Sprintf("arn:aws:s3:::oc-sim-%s/*", runID),
			},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func (g *iamGroup) setupSimulate(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-sim-user-%s", t.RunID)
	if _, err := g.cl().CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(name)}); err != nil {
		return err
	}
	if _, err := g.cl().PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(name),
		PolicyName:     aws.String("sim-allow-read"),
		PolicyDocument: aws.String(iamSimPolicy(t.RunID)),
	}); err != nil {
		return err
	}
	t.Set("iam_sim_user", name)
	return nil
}

func (g *iamGroup) teardownSimulate(ctx context.Context, t *harness.TestContext) error {
	name := t.GetString("iam_sim_user")
	if name == "" {
		return nil
	}
	g.cl().DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{ //nolint:errcheck
		UserName: aws.String(name), PolicyName: aws.String("sim-allow-read"),
	})
	g.cl().DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(name)}) //nolint:errcheck
	return nil
}

func (g *iamGroup) simResource(t *harness.TestContext) string {
	return fmt.Sprintf("arn:aws:s3:::oc-sim-%s/report.csv", t.RunID)
}

func (g *iamGroup) SimulateCustomPolicyAllowed(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{iamSimPolicy(t.RunID)},
		ActionNames:     []string{"s3:GetObject"},
		ResourceArns:    []string{g.simResource(t)},
	})
	if err != nil {
		return err
	}
	if len(resp.EvaluationResults) == 0 {
		return fmt.Errorf("SimulateCustomPolicy: missing EvaluationResults")
	}
	if got := string(resp.EvaluationResults[0].EvalDecision); got != "allowed" {
		return fmt.Errorf("SimulateCustomPolicy: EvalDecision = %s, want allowed", got)
	}
	if got := aws.ToString(resp.EvaluationResults[0].EvalActionName); got != "s3:GetObject" {
		return fmt.Errorf("SimulateCustomPolicy: EvalActionName = %s, want s3:GetObject", got)
	}
	return nil
}

func (g *iamGroup) SimulateCustomPolicyImplicitDeny(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{iamSimPolicy(t.RunID)},
		ActionNames:     []string{"s3:PutObject"},
		ResourceArns:    []string{g.simResource(t)},
	})
	if err != nil {
		return err
	}
	if len(resp.EvaluationResults) == 0 {
		return fmt.Errorf("SimulateCustomPolicy: missing EvaluationResults")
	}
	if got := string(resp.EvaluationResults[0].EvalDecision); got != "implicitDeny" {
		return fmt.Errorf("SimulateCustomPolicy: EvalDecision = %s, want implicitDeny", got)
	}
	return nil
}

func (g *iamGroup) SimulateCustomPolicyExplicitDeny(ctx context.Context, t *harness.TestContext) error {
	doc := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{"Effect": "Allow", "Action": "s3:*", "Resource": "*"},
			{"Effect": "Deny", "Action": "s3:DeleteObject", "Resource": "*"},
		},
	}
	raw, _ := json.Marshal(doc)
	resp, err := g.cl().SimulateCustomPolicy(ctx, &iam.SimulateCustomPolicyInput{
		PolicyInputList: []string{string(raw)},
		ActionNames:     []string{"s3:DeleteObject"},
		ResourceArns:    []string{g.simResource(t)},
	})
	if err != nil {
		return err
	}
	if len(resp.EvaluationResults) == 0 {
		return fmt.Errorf("SimulateCustomPolicy: missing EvaluationResults")
	}
	if got := string(resp.EvaluationResults[0].EvalDecision); got != "explicitDeny" {
		return fmt.Errorf("SimulateCustomPolicy: EvalDecision = %s, want explicitDeny", got)
	}
	return nil
}

func (g *iamGroup) SimulatePrincipalPolicyAllowed(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(fmt.Sprintf("arn:aws:iam::000000000000:user/%s", t.GetString("iam_sim_user"))),
		ActionNames:     []string{"s3:GetObject"},
		ResourceArns:    []string{g.simResource(t)},
	})
	if err != nil {
		return err
	}
	if len(resp.EvaluationResults) == 0 {
		return fmt.Errorf("SimulatePrincipalPolicy: missing EvaluationResults")
	}
	result := resp.EvaluationResults[0]
	if got := string(result.EvalDecision); got != "allowed" {
		return fmt.Errorf("SimulatePrincipalPolicy: EvalDecision = %s, want allowed", got)
	}
	for _, m := range result.MatchedStatements {
		if aws.ToString(m.SourcePolicyId) == "sim-allow-read" {
			return nil
		}
	}
	return fmt.Errorf("SimulatePrincipalPolicy: MatchedStatements missing sim-allow-read")
}

func (g *iamGroup) SimulatePrincipalPolicyImplicitDeny(ctx context.Context, t *harness.TestContext) error {
	resp, err := g.cl().SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(fmt.Sprintf("arn:aws:iam::000000000000:user/%s", t.GetString("iam_sim_user"))),
		ActionNames:     []string{"s3:DeleteObject"},
		ResourceArns:    []string{g.simResource(t)},
	})
	if err != nil {
		return err
	}
	if len(resp.EvaluationResults) == 0 {
		return fmt.Errorf("SimulatePrincipalPolicy: missing EvaluationResults")
	}
	if got := string(resp.EvaluationResults[0].EvalDecision); got != "implicitDeny" {
		return fmt.Errorf("SimulatePrincipalPolicy: EvalDecision = %s, want implicitDeny", got)
	}
	return nil
}
