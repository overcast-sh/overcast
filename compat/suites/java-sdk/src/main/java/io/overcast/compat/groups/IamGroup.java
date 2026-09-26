package io.overcast.compat.groups;

import io.overcast.compat.clients.AwsClients;
import io.overcast.compat.harness.Assertions;
import io.overcast.compat.harness.TestContext;
import io.overcast.compat.harness.TestFn;
import software.amazon.awssdk.services.iam.IamClient;
import software.amazon.awssdk.services.iam.model.*;
import software.amazon.awssdk.services.iam.model.PolicyScopeType;

import java.util.List;
import java.util.Map;

/**
 * IAM compatibility test group.
 *
 * <p>Groups: iam-policies, iam-groups, iam-simulate. iam-users and iam-roles
 * resolve through their authored scenarios (compat/model/authored/iam-users.json,
 * compat/model/authored/iam-roles.json).
 */
public final class IamGroup implements ServiceGroup {

    private static final String BASIC_ASSUME_ROLE_POLICY = """
            {
              "Version": "2012-10-17",
              "Statement": [{
                "Effect": "Allow",
                "Principal": {"Service": "lambda.amazonaws.com"},
                "Action": "sts:AssumeRole"
              }]
            }
            """;

    private static final String INLINE_POLICY = """
            {
              "Version": "2012-10-17",
              "Statement": [{
                "Effect": "Allow",
                "Action": "s3:ListAllMyBuckets",
                "Resource": "*"
              }]
            }
            """;

    /**
     * A document AWS refuses with MalformedPolicyDocument: "Statements must
     * include either an Action or NotAction element" (IAM User Guide,
     * reference_policies_elements_action.html). Every writer that takes a
     * document names that error (IAM API Reference, API_CreatePolicy.html and
     * API_CreateRole.html, Errors).
     */
    private static final String MALFORMED_POLICY = """
            {
              "Version": "2012-10-17",
              "Statement": [{
                "Effect": "Allow",
                "Resource": "*"
              }]
            }
            """;

    /**
     * The tag set the policy fixture is created with, and the one GetPolicy
     * must hand back on the resource itself.
     */
    private static final List<Tag> RESOURCE_TAGS = List.of(
            Tag.builder().key("owner").value("compat").build(),
            Tag.builder().key("stage").value("dev").build());

    private final AwsClients clients;

    public IamGroup(AwsClients clients) {
        this.clients = clients;
    }

    private IamClient iam() { return clients.iam(); }

    @Override
    public Map<String, TestFn> impls() {
        return Map.ofEntries(
                Map.entry("iam-policies:CreatePolicy",                        this::createIamPolicy),
                Map.entry("iam-policies:CreatePolicyMalformedDocument",       this::createPolicyMalformedDocument),
                Map.entry("iam-policies:GetPolicy",                           this::getIamPolicy),
                Map.entry("iam-policies:GetPolicyReturnsTags",                this::getPolicyReturnsTags),
                Map.entry("iam-policies:ListPolicies",                        this::listIamPolicies),
                Map.entry("iam-policies:GetPolicyAttachmentCountAfterAttach", this::getPolicyAttachmentCountAfterAttach),
                Map.entry("iam-policies:GetPolicyAttachmentCountAfterDetach", this::getPolicyAttachmentCountAfterDetach),
                Map.entry("iam-policies:DeletePolicy",                        this::deleteIamPolicy),
                Map.entry("iam-groups:CreateGroup",                           this::createGroup),
                Map.entry("iam-groups:GetGroup",                              this::getGroup),
                Map.entry("iam-groups:AddUserToGroup",                        this::addUserToGroup),
                Map.entry("iam-groups:ListGroupsForUser",                     this::listGroupsForUser),
                Map.entry("iam-groups:RemoveUserFromGroup",                   this::removeUserFromGroup),
                Map.entry("iam-groups:DeleteGroup",                           this::deleteGroup),
                Map.entry("iam-simulate:SimulateCustomPolicyAllowed",         this::simulateCustomPolicyAllowed),
                Map.entry("iam-simulate:SimulateCustomPolicyImplicitDeny",    this::simulateCustomPolicyImplicitDeny),
                Map.entry("iam-simulate:SimulateCustomPolicyExplicitDeny",    this::simulateCustomPolicyExplicitDeny),
                Map.entry("iam-simulate:SimulatePrincipalPolicyAllowed",      this::simulatePrincipalPolicyAllowed),
                Map.entry("iam-simulate:SimulatePrincipalPolicyImplicitDeny", this::simulatePrincipalPolicyImplicitDeny)
        );
    }

    @Override
    public Map<String, TestFn> setups() {
        return Map.ofEntries(
                Map.entry("iam-policies", this::setupPolicies),
                Map.entry("iam-groups",   this::setupIamGroups),
                Map.entry("iam-simulate", this::setupSimulate)
        );
    }

    @Override
    public Map<String, TestFn> teardowns() {
        return Map.ofEntries(
                Map.entry("iam-policies", this::teardownPolicies),
                Map.entry("iam-groups",   this::teardownIamGroups),
                Map.entry("iam-simulate", this::teardownSimulate)
        );
    }

    // ── iam-policies ──────────────────────────────────────────────────────────

    private void setupPolicies(TestContext ctx) {
        ctx.set("iamManagedPolicyName", "compat-policy-" + ctx.runId());
        // The counter tests attach this group's own policy to a role of its
        // own, so AttachmentCount moves for a customer managed policy rather
        // than for the AWS managed one iam-roles attaches.
        String role = "compat-policy-role-" + ctx.runId();
        iam().createRole(r -> r.roleName(role).assumeRolePolicyDocument(BASIC_ASSUME_ROLE_POLICY));
        ctx.set("iamPolicyRole", role);
    }

    private void teardownPolicies(TestContext ctx) {
        String arn = ctx.getString("managedPolicyArn");
        String role = ctx.getString("iamPolicyRole");
        if (role != null) {
            if (arn != null) {
                try { iam().detachRolePolicy(r -> r.roleName(role).policyArn(arn)); } catch (Exception ignored) {}
            }
            try { iam().deleteRole(r -> r.roleName(role)); } catch (Exception ignored) {}
        }
        if (arn == null) return;
        try { iam().deletePolicy(r -> r.policyArn(arn)); } catch (Exception ignored) {}
    }

    private void createIamPolicy(TestContext ctx) throws Exception {
        String name = ctx.getString("iamManagedPolicyName");
        var resp = iam().createPolicy(r -> r.policyName(name)
                .policyDocument(INLINE_POLICY)
                .tags(RESOURCE_TAGS));
        Assertions.assertNotBlank(resp.policy().policyId(), "CreateIamPolicy: policyId is blank");
        ctx.set("managedPolicyArn", resp.policy().arn());
    }

    private void getIamPolicy(TestContext ctx) throws Exception {
        String arn = ctx.getString("managedPolicyArn");
        var resp = iam().getPolicy(r -> r.policyArn(arn));
        Assertions.assertNotBlank(resp.policy().policyName(), "GetIamPolicy: policyName is blank");
    }

    private void listIamPolicies(TestContext ctx) throws Exception {
        String name = ctx.getString("iamManagedPolicyName");
        var resp = iam().listPolicies(r -> r.scope(PolicyScopeType.LOCAL).maxItems(1000));
        boolean found = resp.policies().stream().anyMatch(p -> name.equals(p.policyName()));
        Assertions.assertTrue(found, "ListIamPolicies: created policy " + name + " not found in list");
    }

    /** The same refusal on the identity-policy writer. */
    private void createPolicyMalformedDocument(TestContext ctx) throws Exception {
        String name = ctx.getString("iamManagedPolicyName") + "-malformed";
        try {
            iam().createPolicy(r -> r.policyName(name).policyDocument(MALFORMED_POLICY));
        } catch (IamException e) {
            assertMalformedPolicyDocument("CreatePolicyMalformedDocument", e);
            return;
        }
        throw new AssertionError(
                "CreatePolicyMalformedDocument: expected MalformedPolicyDocument, call succeeded");
    }

    /** Tags given to CreatePolicy come back on the policy (API_Policy.html). */
    private void getPolicyReturnsTags(TestContext ctx) throws Exception {
        String arn = ctx.getString("managedPolicyArn");
        var resp = iam().getPolicy(r -> r.policyArn(arn));
        assertResourceTags("GetPolicyReturnsTags", resp.policy().tags());
    }

    /** Reads AttachmentCount back through GetPolicy. */
    private int attachmentCount(TestContext ctx, String op) {
        String arn = ctx.getString("managedPolicyArn");
        var resp = iam().getPolicy(r -> r.policyArn(arn));
        Integer count = resp.policy().attachmentCount();
        Assertions.assertNotNull(count, op + ": GetPolicy returned no AttachmentCount");
        return count;
    }

    /**
     * AttachmentCount moves 0 to 1. "The number of entities (users, groups, and
     * roles) that the policy is attached to" (IAM API Reference,
     * API_Policy.html) is what a cleanup script reads before deleting a policy,
     * so a stuck 0 deletes something in use.
     */
    private void getPolicyAttachmentCountAfterAttach(TestContext ctx) throws Exception {
        final String op = "GetPolicyAttachmentCountAfterAttach";
        Assertions.assertEquals(0, attachmentCount(ctx, op), op + ": AttachmentCount before the attach");
        String arn = ctx.getString("managedPolicyArn");
        String role = ctx.getString("iamPolicyRole");
        Assertions.assertNotNull(role, op + ": no role from setup");
        iam().attachRolePolicy(r -> r.roleName(role).policyArn(arn));
        Assertions.assertEquals(1, attachmentCount(ctx, op),
                op + ": AttachmentCount after attaching to one role");
    }

    /** The counter moves back to 0, which a never-decremented one would fail. */
    private void getPolicyAttachmentCountAfterDetach(TestContext ctx) throws Exception {
        final String op = "GetPolicyAttachmentCountAfterDetach";
        String arn = ctx.getString("managedPolicyArn");
        String role = ctx.getString("iamPolicyRole");
        Assertions.assertNotNull(role, op + ": no role from setup");
        iam().detachRolePolicy(r -> r.roleName(role).policyArn(arn));
        Assertions.assertEquals(0, attachmentCount(ctx, op),
                op + ": AttachmentCount after the detach");
    }

    private void deleteIamPolicy(TestContext ctx) throws Exception {
        String arn = ctx.getString("managedPolicyArn");
        iam().deletePolicy(r -> r.policyArn(arn));
        ctx.set("managedPolicyArn", null);
    }

    // ── iam-groups ────────────────────────────────────────────────────────────

    private void setupIamGroups(TestContext ctx) throws Exception {
        String grp  = "compat-grp-" + ctx.runId();
        String user = "compat-grpu-" + ctx.runId();
        iam().createGroup(r -> r.groupName(grp));
        iam().createUser(r -> r.userName(user));
        ctx.set("iamGroupName", grp);
        ctx.set("iamGroupUser", user);
    }

    private void teardownIamGroups(TestContext ctx) {
        String grp  = ctx.getString("iamGroupName");
        String user = ctx.getString("iamGroupUser");
        if (user != null)
            try { iam().removeUserFromGroup(r -> r.groupName(grp).userName(user)); } catch (Exception ignored) {}
        if (grp != null)
            try { iam().deleteGroup(r -> r.groupName(grp)); } catch (Exception ignored) {}
        if (user != null)
            try { iam().deleteUser(r -> r.userName(user)); } catch (Exception ignored) {}
    }

    private void createGroup(TestContext ctx) {
        Assertions.assertNotBlank(ctx.getString("iamGroupName"), "iamGroupName");
    }

    private void getGroup(TestContext ctx) throws Exception {
        String grp = ctx.getString("iamGroupName");
        var resp = iam().getGroup(r -> r.groupName(grp));
        Assertions.assertEquals(grp, resp.group().groupName(), "GetGroup: groupName mismatch");
    }

    private void addUserToGroup(TestContext ctx) throws Exception {
        String grp  = ctx.getString("iamGroupName");
        String user = ctx.getString("iamGroupUser");
        iam().addUserToGroup(r -> r.groupName(grp).userName(user));
    }

    private void listGroupsForUser(TestContext ctx) throws Exception {
        String user = ctx.getString("iamGroupUser");
        var resp = iam().listGroupsForUser(r -> r.userName(user));
        boolean found = resp.groups().stream()
                .anyMatch(g -> g.groupName().equals(ctx.getString("iamGroupName")));
        Assertions.assertTrue(found, "ListGroupsForUser: group not found for user");
    }

    private void removeUserFromGroup(TestContext ctx) throws Exception {
        String grp  = ctx.getString("iamGroupName");
        String user = ctx.getString("iamGroupUser");
        iam().removeUserFromGroup(r -> r.groupName(grp).userName(user));
    }

    private void deleteGroup(TestContext ctx) throws Exception {
        String grp = ctx.getString("iamGroupName");
        iam().deleteGroup(r -> r.groupName(grp));
        ctx.set("iamGroupName", null);
    }

    // ── iam-simulate ──────────────────────────────────────────────────────────

    /** The identity policy the simulate group evaluates: read one run-scoped prefix, nothing else. */
    private String simPolicy(TestContext ctx) {
        return """
                {
                  "Version": "2012-10-17",
                  "Statement": [{
                    "Effect": "Allow",
                    "Action": "s3:GetObject",
                    "Resource": "arn:aws:s3:::compat-sim-%s/*"
                  }]
                }
                """.formatted(ctx.runId());
    }

    private String simResource(TestContext ctx) {
        return "arn:aws:s3:::compat-sim-" + ctx.runId() + "/report.csv";
    }

    private void setupSimulate(TestContext ctx) {
        String name = "compat-sim-user-" + ctx.runId();
        iam().createUser(r -> r.userName(name));
        iam().putUserPolicy(r -> r.userName(name)
                .policyName("sim-allow-read")
                .policyDocument(simPolicy(ctx)));
        ctx.set("iamSimUser", name);
    }

    private void teardownSimulate(TestContext ctx) {
        String name = ctx.getString("iamSimUser");
        if (name == null) return;
        try { iam().deleteUserPolicy(r -> r.userName(name).policyName("sim-allow-read")); } catch (Exception ignored) {}
        try { iam().deleteUser(r -> r.userName(name)); } catch (Exception ignored) {}
    }

    private void simulateCustomPolicyAllowed(TestContext ctx) throws Exception {
        var resp = iam().simulateCustomPolicy(r -> r
                .policyInputList(simPolicy(ctx))
                .actionNames("s3:GetObject")
                .resourceArns(simResource(ctx)));
        Assertions.assertTrue(!resp.evaluationResults().isEmpty(),
                "SimulateCustomPolicy: no evaluation results");
        var result = resp.evaluationResults().get(0);
        Assertions.assertEquals("allowed", result.evalDecisionAsString(),
                "SimulateCustomPolicy: wrong decision");
        Assertions.assertEquals("s3:GetObject", result.evalActionName(),
                "SimulateCustomPolicy: wrong evalActionName");
    }

    private void simulateCustomPolicyImplicitDeny(TestContext ctx) throws Exception {
        var resp = iam().simulateCustomPolicy(r -> r
                .policyInputList(simPolicy(ctx))
                .actionNames("s3:PutObject")
                .resourceArns(simResource(ctx)));
        Assertions.assertTrue(!resp.evaluationResults().isEmpty(),
                "SimulateCustomPolicy: no evaluation results");
        Assertions.assertEquals("implicitDeny", resp.evaluationResults().get(0).evalDecisionAsString(),
                "SimulateCustomPolicy: uncovered action should be implicitDeny");
    }

    private void simulateCustomPolicyExplicitDeny(TestContext ctx) throws Exception {
        String doc = """
                {
                  "Version": "2012-10-17",
                  "Statement": [
                    {"Effect": "Allow", "Action": "s3:*", "Resource": "*"},
                    {"Effect": "Deny", "Action": "s3:DeleteObject", "Resource": "*"}
                  ]
                }
                """;
        var resp = iam().simulateCustomPolicy(r -> r
                .policyInputList(doc)
                .actionNames("s3:DeleteObject")
                .resourceArns(simResource(ctx)));
        Assertions.assertTrue(!resp.evaluationResults().isEmpty(),
                "SimulateCustomPolicy: no evaluation results");
        Assertions.assertEquals("explicitDeny", resp.evaluationResults().get(0).evalDecisionAsString(),
                "SimulateCustomPolicy: explicit deny should win");
    }

    private void simulatePrincipalPolicyAllowed(TestContext ctx) throws Exception {
        String arn = "arn:aws:iam::000000000000:user/" + ctx.getString("iamSimUser");
        var resp = iam().simulatePrincipalPolicy(r -> r
                .policySourceArn(arn)
                .actionNames("s3:GetObject")
                .resourceArns(simResource(ctx)));
        Assertions.assertTrue(!resp.evaluationResults().isEmpty(),
                "SimulatePrincipalPolicy: no evaluation results");
        var result = resp.evaluationResults().get(0);
        Assertions.assertEquals("allowed", result.evalDecisionAsString(),
                "SimulatePrincipalPolicy: wrong decision");
        boolean matched = result.matchedStatements().stream()
                .anyMatch(s -> "sim-allow-read".equals(s.sourcePolicyId()));
        Assertions.assertTrue(matched,
                "SimulatePrincipalPolicy: matchedStatements missing sim-allow-read");
    }

    private void simulatePrincipalPolicyImplicitDeny(TestContext ctx) throws Exception {
        String arn = "arn:aws:iam::000000000000:user/" + ctx.getString("iamSimUser");
        var resp = iam().simulatePrincipalPolicy(r -> r
                .policySourceArn(arn)
                .actionNames("s3:DeleteObject")
                .resourceArns(simResource(ctx)));
        Assertions.assertTrue(!resp.evaluationResults().isEmpty(),
                "SimulatePrincipalPolicy: no evaluation results");
        Assertions.assertEquals("implicitDeny", resp.evaluationResults().get(0).evalDecisionAsString(),
                "SimulatePrincipalPolicy: uncovered action should be implicitDeny");
    }

    // ── shared assertions ─────────────────────────────────────────────────────

    /**
     * Checks both halves of the error contract: the code AWS's model names, and
     * the 400 the Query protocol binds it to.
     */
    private static void assertMalformedPolicyDocument(String op, IamException e) {
        String code = e.awsErrorDetails() == null ? "" : e.awsErrorDetails().errorCode();
        Assertions.assertEquals("MalformedPolicyDocument", code, op + ": unexpected error code");
        Assertions.assertEquals(400, e.statusCode(),
                op + ": expected HTTP 400 for MalformedPolicyDocument");
    }

    /** Checks the two fixture tags on a resource returned by a Get* call. */
    private static void assertResourceTags(String op, List<Tag> tags) {
        for (Tag want : RESOURCE_TAGS) {
            boolean found = tags != null && tags.stream()
                    .anyMatch(t -> want.key().equals(t.key()) && want.value().equals(t.value()));
            Assertions.assertTrue(found,
                    op + ": tag " + want.key() + "=" + want.value() + " not on the resource (tags: " + tags + ")");
        }
    }
}
