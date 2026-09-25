package groups

import (
	"context"
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// IAM returns the IAM service group.
func IAM() ServiceGroup {
	g := &iamGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// iam-policies
			"iam-policies:CreatePolicy":                        g.CreatePolicy,
			"iam-policies:CreatePolicyMalformedDocument":       g.CreatePolicyMalformedDocument,
			"iam-policies:GetPolicy":                           g.GetPolicy,
			"iam-policies:GetPolicyReturnsTags":                g.GetPolicyReturnsTags,
			"iam-policies:ListPolicies":                        g.ListPolicies,
			"iam-policies:GetPolicyAttachmentCountAfterAttach": g.GetPolicyAttachmentCountAfterAttach,
			"iam-policies:GetPolicyAttachmentCountAfterDetach": g.GetPolicyAttachmentCountAfterDetach,
			"iam-policies:DeletePolicy":                        g.DeletePolicy,
			// iam-groups
			"iam-groups:CreateGroup":         g.CreateGroup,
			"iam-groups:AddUserToGroup":      g.AddUserToGroup,
			"iam-groups:ListGroupsForUser":   g.ListGroupsForUser,
			"iam-groups:RemoveUserFromGroup": g.RemoveUserFromGroup,
			"iam-groups:GetGroup":            g.GetGroup,
			"iam-groups:DeleteGroup":         g.DeleteGroup,
			// iam-simulate
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
			"iam-groups":   g.teardownIAMGroups,
			"iam-simulate": g.teardownSimulate,
		},
	}
}

// One Namer per IAM resource type keeps names deterministic and descriptive.
var (
	iamPolicyNamer    = harness.NewNamer("iam-pol")
	iamGroupNamer     = harness.NewNamer("iam-grp")
	iamPolRoleNamer   = harness.NewNamer("iam-pr") // separate role for iam-policies group
	iamGrpUserNamer   = harness.NewNamer("iam-gu") // separate user for iam-groups group
	iamSimUserNamer   = harness.NewNamer("iam-sim-u")
	iamSimBucketNamer = harness.NewNamer("iam-sim-b") // ARN subject only; no bucket is created
)

type iamGroup struct{}

func (g *iamGroup) assumePolicy() string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
}

func (g *iamGroup) s3PolicyDoc() string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"*"}]}`
}

// iamMalformedPolicy is a document AWS refuses with MalformedPolicyDocument:
// "Statements must include either an Action or NotAction element" (IAM User
// Guide, reference_policies_elements_action.html). Every writer that takes a
// document names that error (IAM API Reference, API_CreatePolicy.html and
// API_CreateRole.html, Errors).
const iamMalformedPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Resource":"*"}]}`

// iamTagArgs is the --tags shorthand the policy fixture is created with, and
// the tag set GetPolicy must hand back on the resource.
var iamTagArgs = []string{"Key=owner,Value=compat", "Key=stage,Value=dev"}

// expectIAMFailure runs a command that must fail and asserts both halves of the
// error contract: the code AWS's model names, and the HTTP status the Query
// protocol binds it to.
func expectIAMFailure(t *harness.TestContext, testName, wantCode string, wantStatus int, args ...string) error {
	status, err := awscli.RunStatus(t.Endpoint, t.Region, args...)
	if err == nil {
		return fmt.Errorf("%s: expected %s, got success", testName, wantCode)
	}
	if !strings.Contains(err.Error(), wantCode) {
		return fmt.Errorf("%s: expected %s, got %v", testName, wantCode, err)
	}
	if status != wantStatus {
		return fmt.Errorf("%s: expected HTTP %d for %s, got %d", testName, wantStatus, wantCode, status)
	}
	return nil
}

// assertResourceTags checks the two fixture tags on a Tags member of a Get*
// response, which is the CLI's rendering of the tag list on the resource.
func assertResourceTags(testName string, raw any) error {
	got := map[string]string{}
	items, _ := raw.([]any)
	for _, item := range items {
		tag, _ := item.(map[string]any)
		key, _ := tag["Key"].(string)
		value, _ := tag["Value"].(string)
		got[key] = value
	}
	for key, value := range map[string]string{"owner": "compat", "stage": "dev"} {
		if got[key] != value {
			return fmt.Errorf("%s: tag %s = %q, want %q (tags: %v)", testName, key, got[key], value, got)
		}
	}
	return nil
}

// iamAttachmentCount reads AttachmentCount back through get-policy.
func (g *iamGroup) iamAttachmentCount(t *harness.TestContext, testName string) (float64, error) {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "get-policy", "--policy-arn", t.GetString("policy_arn"))
	if err != nil {
		return 0, err
	}
	policy, _ := out["Policy"].(map[string]any)
	count, ok := policy["AttachmentCount"].(float64)
	if !ok {
		return 0, fmt.Errorf("%s: get-policy returned no AttachmentCount: %v", testName, policy)
	}
	return count, nil
}

// ─── iam-policies ────────────────────────────────────────────────────────────

func (g *iamGroup) setupPolicies(_ context.Context, t *harness.TestContext) error {
	// Create a role so we can test policy attachment.
	// iamPolRoleNamer gives this group a role of its own, so it doesn't collide
	// with the iam-roles scenario that runs in parallel.
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "create-role",
		"--role-name", iamPolRoleNamer.Name(t),
		"--assume-role-policy-document", g.assumePolicy(),
	)
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	if err != nil {
		// Role already exists — fetch its ARN.
		out, err = awscli.RunOutput(t.Endpoint, t.Region,
			"iam", "get-role", "--role-name", iamPolRoleNamer.Name(t))
		if err != nil {
			return err
		}
	}
	role, _ := out["Role"].(map[string]any)
	arn, _ := role["Arn"].(string)
	t.Set("role_arn", arn)
	return nil
}

func (g *iamGroup) CreatePolicy(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region, append([]string{
		"iam", "create-policy",
		"--policy-name", iamPolicyNamer.Name(t),
		"--policy-document", g.s3PolicyDoc(),
		"--tags",
	}, iamTagArgs...)...)
	if err != nil {
		return err
	}
	policy, _ := out["Policy"].(map[string]any)
	arn, _ := policy["Arn"].(string)
	t.Set("policy_arn", arn)
	return nil
}

func (g *iamGroup) GetPolicy(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("policy_arn")
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "get-policy",
		"--policy-arn", arn,
	)
	return err
}

func (g *iamGroup) ListPolicies(_ context.Context, t *harness.TestContext) error {
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "list-policies",
		"--scope", "Local",
	)
	return err
}

// CreatePolicyMalformedDocument pins the same refusal on the identity-policy
// writer.
func (g *iamGroup) CreatePolicyMalformedDocument(_ context.Context, t *harness.TestContext) error {
	return expectIAMFailure(t, "iam CreatePolicyMalformedDocument", "MalformedPolicyDocument", 400,
		"iam", "create-policy",
		"--policy-name", iamPolicyNamer.Name(t)+"-malformed",
		"--policy-document", iamMalformedPolicy,
	)
}

// GetPolicyReturnsTags pins Tags on the policy resource (API_Policy.html).
func (g *iamGroup) GetPolicyReturnsTags(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "get-policy", "--policy-arn", t.GetString("policy_arn"))
	if err != nil {
		return err
	}
	policy, _ := out["Policy"].(map[string]any)
	return assertResourceTags("iam GetPolicyReturnsTags", policy["Tags"])
}

// GetPolicyAttachmentCountAfterAttach pins AttachmentCount moving 0 to 1.
// "The number of entities (users, groups, and roles) that the policy is
// attached to" (IAM API Reference, API_Policy.html) is what a cleanup script
// reads before deleting a policy, so a stuck 0 deletes something in use.
func (g *iamGroup) GetPolicyAttachmentCountAfterAttach(_ context.Context, t *harness.TestContext) error {
	const testName = "iam GetPolicyAttachmentCountAfterAttach"
	before, err := g.iamAttachmentCount(t, testName)
	if err != nil {
		return err
	}
	if before != 0 {
		return fmt.Errorf("%s: AttachmentCount = %v before the attach, want 0", testName, before)
	}
	if err := awscli.Run(t.Endpoint, t.Region,
		"iam", "attach-role-policy",
		"--role-name", iamPolRoleNamer.Name(t),
		"--policy-arn", t.GetString("policy_arn"),
	); err != nil {
		return err
	}
	after, err := g.iamAttachmentCount(t, testName)
	if err != nil {
		return err
	}
	if after != 1 {
		return fmt.Errorf("%s: AttachmentCount = %v after attaching to one role, want 1", testName, after)
	}
	return nil
}

// GetPolicyAttachmentCountAfterDetach pins the counter moving back to 0, which
// a counter incremented but never decremented would fail.
func (g *iamGroup) GetPolicyAttachmentCountAfterDetach(_ context.Context, t *harness.TestContext) error {
	const testName = "iam GetPolicyAttachmentCountAfterDetach"
	if err := awscli.Run(t.Endpoint, t.Region,
		"iam", "detach-role-policy",
		"--role-name", iamPolRoleNamer.Name(t),
		"--policy-arn", t.GetString("policy_arn"),
	); err != nil {
		return err
	}
	after, err := g.iamAttachmentCount(t, testName)
	if err != nil {
		return err
	}
	if after != 0 {
		return fmt.Errorf("%s: AttachmentCount = %v after the detach, want 0", testName, after)
	}
	return nil
}

func (g *iamGroup) DeletePolicy(_ context.Context, t *harness.TestContext) error {
	arn := t.GetString("policy_arn")
	return awscli.Run(t.Endpoint, t.Region, "iam", "delete-policy", "--policy-arn", arn)
}

func (g *iamGroup) teardownPolicies(_ context.Context, t *harness.TestContext) error {
	if policyArn := t.GetString("policy_arn"); policyArn != "" {
		awscli.Run(t.Endpoint, t.Region, "iam", "detach-role-policy", //nolint:errcheck
			"--role-name", iamPolRoleNamer.Name(t), "--policy-arn", policyArn)
		awscli.Run(t.Endpoint, t.Region, "iam", "delete-policy", "--policy-arn", policyArn) //nolint:errcheck
	}
	awscli.Run(t.Endpoint, t.Region, "iam", "delete-role", "--role-name", iamPolRoleNamer.Name(t)) //nolint:errcheck
	// The refused create-policy must leave nothing; delete it anyway so a
	// regression that stored it does not leak a policy into the next run.
	awscli.Run(t.Endpoint, t.Region, "iam", "delete-policy", //nolint:errcheck
		"--policy-arn", "arn:aws:iam::000000000000:policy/"+iamPolicyNamer.Name(t)+"-malformed")
	return nil
}

// ─── iam-groups ──────────────────────────────────────────────────────────────

func (g *iamGroup) setupGroups(_ context.Context, t *harness.TestContext) error {
	// Create a user to add to the group.
	// iamGrpUserNamer gives this group a user of its own, so it doesn't collide
	// with the iam-users scenario that runs in parallel.
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "create-user", "--user-name", iamGrpUserNamer.Name(t),
	)
	if err != nil && isAlreadyExists(err) {
		return nil
	}
	return err
}

func (g *iamGroup) CreateGroup(_ context.Context, t *harness.TestContext) error {
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "create-group",
		"--group-name", iamGroupNamer.Name(t),
	)
	return err
}

func (g *iamGroup) AddUserToGroup(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region,
		"iam", "add-user-to-group",
		"--group-name", iamGroupNamer.Name(t),
		"--user-name", iamGrpUserNamer.Name(t),
	)
}

func (g *iamGroup) ListGroupsForUser(_ context.Context, t *harness.TestContext) error {
	_, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "list-groups-for-user",
		"--user-name", iamGrpUserNamer.Name(t),
	)
	return err
}

func (g *iamGroup) RemoveUserFromGroup(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region,
		"iam", "remove-user-from-group",
		"--group-name", iamGroupNamer.Name(t),
		"--user-name", iamGrpUserNamer.Name(t),
	)
}

func (g *iamGroup) GetGroup(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "get-group",
		"--group-name", iamGroupNamer.Name(t),
	)
	if err != nil {
		return err
	}
	grp, _ := out["Group"].(map[string]any)
	if grp == nil || grp["GroupName"] != iamGroupNamer.Name(t) {
		return fmt.Errorf("iam GetGroup: wrong group name")
	}
	return nil
}

func (g *iamGroup) DeleteGroup(_ context.Context, t *harness.TestContext) error {
	return awscli.Run(t.Endpoint, t.Region,
		"iam", "delete-group",
		"--group-name", iamGroupNamer.Name(t),
	)
}

func (g *iamGroup) teardownIAMGroups(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "iam", "remove-user-from-group", //nolint:errcheck
		"--group-name", iamGroupNamer.Name(t), "--user-name", iamGrpUserNamer.Name(t))
	awscli.Run(t.Endpoint, t.Region, "iam", "delete-group", "--group-name", iamGroupNamer.Name(t)) //nolint:errcheck
	awscli.Run(t.Endpoint, t.Region, "iam", "delete-user", "--user-name", iamGrpUserNamer.Name(t)) //nolint:errcheck
	return nil
}

// ─── iam-simulate ────────────────────────────────────────────────────────────

// simPolicy is the identity policy the simulate group evaluates: read access to
// one run-scoped bucket prefix and nothing else.
func (g *iamGroup) simPolicy(t *harness.TestContext) string {
	return fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`,
		iamSimBucketNamer.Name(t))
}

func (g *iamGroup) simResource(t *harness.TestContext) string {
	return fmt.Sprintf("arn:aws:s3:::%s/report.csv", iamSimBucketNamer.Name(t))
}

func (g *iamGroup) setupSimulate(_ context.Context, t *harness.TestContext) error {
	if _, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "create-user", "--user-name", iamSimUserNamer.Name(t),
	); err != nil && !isAlreadyExists(err) {
		return err
	}
	return awscli.Run(t.Endpoint, t.Region,
		"iam", "put-user-policy",
		"--user-name", iamSimUserNamer.Name(t),
		"--policy-name", "sim-allow-read",
		"--policy-document", g.simPolicy(t),
	)
}

func (g *iamGroup) teardownSimulate(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, //nolint:errcheck
		"iam", "delete-user-policy",
		"--user-name", iamSimUserNamer.Name(t),
		"--policy-name", "sim-allow-read")
	awscli.Run(t.Endpoint, t.Region, //nolint:errcheck
		"iam", "delete-user", "--user-name", iamSimUserNamer.Name(t))
	return nil
}

// simDecision pulls the decision out of the first evaluation result.
func simDecision(out map[string]any) (map[string]any, string, error) {
	results, _ := out["EvaluationResults"].([]any)
	if len(results) == 0 {
		return nil, "", fmt.Errorf("simulate: missing EvaluationResults")
	}
	first, _ := results[0].(map[string]any)
	decision, _ := first["EvalDecision"].(string)
	return first, decision, nil
}

func (g *iamGroup) SimulateCustomPolicyAllowed(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "simulate-custom-policy",
		"--policy-input-list", g.simPolicy(t),
		"--action-names", "s3:GetObject",
		"--resource-arns", g.simResource(t),
	)
	if err != nil {
		return err
	}
	first, decision, err := simDecision(out)
	if err != nil {
		return err
	}
	if decision != "allowed" {
		return fmt.Errorf("SimulateCustomPolicy: EvalDecision = %s, want allowed", decision)
	}
	if action, _ := first["EvalActionName"].(string); action != "s3:GetObject" {
		return fmt.Errorf("SimulateCustomPolicy: EvalActionName = %s, want s3:GetObject", action)
	}
	return nil
}

func (g *iamGroup) SimulateCustomPolicyImplicitDeny(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "simulate-custom-policy",
		"--policy-input-list", g.simPolicy(t),
		"--action-names", "s3:PutObject",
		"--resource-arns", g.simResource(t),
	)
	if err != nil {
		return err
	}
	_, decision, err := simDecision(out)
	if err != nil {
		return err
	}
	if decision != "implicitDeny" {
		return fmt.Errorf("SimulateCustomPolicy: EvalDecision = %s, want implicitDeny", decision)
	}
	return nil
}

func (g *iamGroup) SimulateCustomPolicyExplicitDeny(_ context.Context, t *harness.TestContext) error {
	doc := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"s3:*","Resource":"*"},` +
		`{"Effect":"Deny","Action":"s3:DeleteObject","Resource":"*"}]}`
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "simulate-custom-policy",
		"--policy-input-list", doc,
		"--action-names", "s3:DeleteObject",
		"--resource-arns", g.simResource(t),
	)
	if err != nil {
		return err
	}
	_, decision, err := simDecision(out)
	if err != nil {
		return err
	}
	if decision != "explicitDeny" {
		return fmt.Errorf("SimulateCustomPolicy: EvalDecision = %s, want explicitDeny", decision)
	}
	return nil
}

func (g *iamGroup) SimulatePrincipalPolicyAllowed(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "simulate-principal-policy",
		"--policy-source-arn", fmt.Sprintf("arn:aws:iam::000000000000:user/%s", iamSimUserNamer.Name(t)),
		"--action-names", "s3:GetObject",
		"--resource-arns", g.simResource(t),
	)
	if err != nil {
		return err
	}
	first, decision, err := simDecision(out)
	if err != nil {
		return err
	}
	if decision != "allowed" {
		return fmt.Errorf("SimulatePrincipalPolicy: EvalDecision = %s, want allowed", decision)
	}
	matched, _ := first["MatchedStatements"].([]any)
	for _, m := range matched {
		stmt, _ := m.(map[string]any)
		if id, _ := stmt["SourcePolicyId"].(string); id == "sim-allow-read" {
			return nil
		}
	}
	return fmt.Errorf("SimulatePrincipalPolicy: MatchedStatements missing sim-allow-read")
}

func (g *iamGroup) SimulatePrincipalPolicyImplicitDeny(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"iam", "simulate-principal-policy",
		"--policy-source-arn", fmt.Sprintf("arn:aws:iam::000000000000:user/%s", iamSimUserNamer.Name(t)),
		"--action-names", "s3:DeleteObject",
		"--resource-arns", g.simResource(t),
	)
	if err != nil {
		return err
	}
	_, decision, err := simDecision(out)
	if err != nil {
		return err
	}
	if decision != "implicitDeny" {
		return fmt.Errorf("SimulatePrincipalPolicy: EvalDecision = %s, want implicitDeny", decision)
	}
	return nil
}
