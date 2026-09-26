using Amazon.IdentityManagement;
using Amazon.IdentityManagement.Model;
using OvercastCompat.Clients;
using OvercastCompat.Harness;

namespace OvercastCompat.Groups;

/// <summary>
/// IAM compatibility groups: iam-policies, iam-groups, iam-simulate. iam-users
/// and iam-roles resolve through their authored scenarios
/// (compat/model/authored/iam-users.json, compat/model/authored/iam-roles.json).
/// </summary>
public sealed class IamGroup(AwsClients clients) : IServiceGroup
{
    public IReadOnlyDictionary<string, TestFn> Impls() => new Dictionary<string, TestFn>(StringComparer.Ordinal)
    {
        // iam-policies
        ["iam-policies:CreatePolicy"] = CreatePolicyAsync,
        ["iam-policies:CreatePolicyMalformedDocument"] = CreatePolicyMalformedDocumentAsync,
        ["iam-policies:GetPolicy"] = GetPolicyAsync,
        ["iam-policies:GetPolicyReturnsTags"] = GetPolicyReturnsTagsAsync,
        ["iam-policies:ListPolicies"] = ListPoliciesAsync,
        ["iam-policies:GetPolicyAttachmentCountAfterAttach"] = GetPolicyAttachmentCountAfterAttachAsync,
        ["iam-policies:GetPolicyAttachmentCountAfterDetach"] = GetPolicyAttachmentCountAfterDetachAsync,
        ["iam-policies:DeletePolicy"] = DeletePolicyAsync,
        // iam-groups
        ["iam-groups:CreateGroup"] = CreateGroupAsync,
        ["iam-groups:AddUserToGroup"] = AddUserToGroupAsync,
        ["iam-groups:ListGroupsForUser"] = ListGroupsForUserAsync,
        ["iam-groups:RemoveUserFromGroup"] = RemoveUserFromGroupAsync,
        ["iam-groups:GetGroup"] = GetGroupAsync,
        ["iam-groups:DeleteGroup"] = DeleteGroupAsync,
        // iam-simulate
        ["iam-simulate:SimulateCustomPolicyAllowed"] = SimulateCustomPolicyAllowedAsync,
        ["iam-simulate:SimulateCustomPolicyImplicitDeny"] = SimulateCustomPolicyImplicitDenyAsync,
        ["iam-simulate:SimulateCustomPolicyExplicitDeny"] = SimulateCustomPolicyExplicitDenyAsync,
        ["iam-simulate:SimulatePrincipalPolicyAllowed"] = SimulatePrincipalPolicyAllowedAsync,
        ["iam-simulate:SimulatePrincipalPolicyImplicitDeny"] = SimulatePrincipalPolicyImplicitDenyAsync,
    };

    public IReadOnlyDictionary<string, SetupFn> Setups() => new Dictionary<string, SetupFn>(StringComparer.Ordinal)
    {
        ["iam-policies"] = SetupPoliciesAsync,
        ["iam-groups"] = SetupGroupsAsync,
        ["iam-simulate"] = SetupSimulateAsync,
    };

    public IReadOnlyDictionary<string, SetupFn> Teardowns() => new Dictionary<string, SetupFn>(StringComparer.Ordinal)
    {
        ["iam-policies"] = TeardownPoliciesAsync,
        ["iam-groups"] = TeardownGroupsAsync,
        ["iam-simulate"] = TeardownSimulateAsync,
    };

    /// <summary>
    /// A document AWS refuses with MalformedPolicyDocument: "Statements must
    /// include either an Action or NotAction element" (IAM User Guide,
    /// reference_policies_elements_action.html). Every writer that takes a
    /// document names that error (IAM API Reference, API_CreatePolicy.html and
    /// API_CreateRole.html, Errors).
    /// </summary>
    private const string MalformedPolicy = @"{""Version"":""2012-10-17"",""Statement"":[{""Effect"":""Allow"",""Resource"":""*""}]}";

    /// <summary>
    /// The tag set the policy fixture is created with, and the one GetPolicy
    /// must hand back on the resource itself.
    /// </summary>
    private static List<Tag> ResourceTags() => new()
    {
        new Tag { Key = "owner", Value = "compat" },
        new Tag { Key = "stage", Value = "dev" },
    };

    /// <summary>
    /// Checks both halves of the error contract: the code AWS's model names,
    /// and the 400 the Query protocol binds it to.
    /// </summary>
    private static void AssertMalformedPolicyDocument(string op, AmazonIdentityManagementServiceException e)
    {
        Assertions.Equal("MalformedPolicyDocument", e.ErrorCode, $"{op}: expected MalformedPolicyDocument but was {e.ErrorCode}");
        Assertions.Equal(System.Net.HttpStatusCode.BadRequest, e.StatusCode, $"{op}: expected HTTP 400 for MalformedPolicyDocument but was {(int)e.StatusCode}");
    }

    /// <summary>Checks the two fixture tags on a resource returned by a Get* call.</summary>
    private static void AssertResourceTags(string op, List<Tag>? tags)
    {
        foreach (var want in ResourceTags())
        {
            Assertions.True(
                tags is not null && tags.Any(t => t.Key == want.Key && t.Value == want.Value),
                $"{op}: tag {want.Key}={want.Value} not on the resource");
        }
    }

    // ── iam-policies ──

    private async Task SetupPoliciesAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-policy";
        var response = await clients.IAM().CreatePolicyAsync(new CreatePolicyRequest
        {
            PolicyName = name,
            PolicyDocument = "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBucket\",\"Resource\":\"*\"}]}",
            Tags = ResourceTags(),
        });
        context.Set("IamPolicyArn", response.Policy.Arn);

        // The counter tests attach this group's own policy to a role of its
        // own, so AttachmentCount moves for a customer managed policy rather
        // than for the AWS managed one iam-roles attaches.
        var roleName = $"{context.RunId}-iam-policy-role";
        await clients.IAM().CreateRoleAsync(new CreateRoleRequest
        {
            RoleName = roleName,
            AssumeRolePolicyDocument = @"{""Version"":""2012-10-17"",""Statement"":[{""Effect"":""Allow"",""Principal"":{""Service"":""lambda.amazonaws.com""},""Action"":""sts:AssumeRole""}]}",
        });
        context.Set("IamPolicyRoleName", roleName);
    }

    private async Task CreatePolicyAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-create-policy";
        var response = await clients.IAM().CreatePolicyAsync(new CreatePolicyRequest
        {
            PolicyName = name,
            PolicyDocument = "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBucket\",\"Resource\":\"*\"}]}",
        });
        var arn = response.Policy.Arn;
        Assertions.NotBlank(arn, "CreatePolicy: Arn");
        try
        {
            var list = await clients.IAM().ListPoliciesAsync(new ListPoliciesRequest());
            Assertions.True(list.Policies.Any(p => p.Arn == arn), $"CreatePolicy: policy {arn} not found in ListPolicies (runId={context.RunId})");
        }
        finally
        {
            try { await clients.IAM().DeletePolicyAsync(new DeletePolicyRequest { PolicyArn = arn }); } catch { }
        }
    }

    private async Task GetPolicyAsync(TestContext context)
    {
        var arn = RequireString(context, "IamPolicyArn");
        var response = await clients.IAM().GetPolicyAsync(new GetPolicyRequest { PolicyArn = arn });
        Assertions.NotBlank(response.Policy.PolicyName, "GetPolicy: PolicyName");
    }

    private async Task ListPoliciesAsync(TestContext context)
    {
        var arn = RequireString(context, "IamPolicyArn");
        var response = await clients.IAM().ListPoliciesAsync(new ListPoliciesRequest());
        Assertions.True(response.Policies.Any(p => p.Arn == arn), $"ListPolicies: policy {arn} not found (runId={context.RunId})");
    }

    /// <summary>The same refusal on the identity-policy writer.</summary>
    private async Task CreatePolicyMalformedDocumentAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-policy-malformed";
        try
        {
            await clients.IAM().CreatePolicyAsync(new CreatePolicyRequest
            {
                PolicyName = name,
                PolicyDocument = MalformedPolicy,
            });
        }
        catch (AmazonIdentityManagementServiceException e)
        {
            AssertMalformedPolicyDocument("CreatePolicyMalformedDocument", e);
            return;
        }

        throw new InvalidOperationException(
            $"CreatePolicyMalformedDocument: expected MalformedPolicyDocument, call succeeded (runId={context.RunId})");
    }

    /// <summary>Tags given to CreatePolicy come back on the policy (API_Policy.html).</summary>
    private async Task GetPolicyReturnsTagsAsync(TestContext context)
    {
        var arn = RequireString(context, "IamPolicyArn");
        var response = await clients.IAM().GetPolicyAsync(new GetPolicyRequest { PolicyArn = arn });
        AssertResourceTags("GetPolicyReturnsTags", response.Policy.Tags);
    }

    /// <summary>Reads AttachmentCount back through GetPolicy.</summary>
    private async Task<int?> AttachmentCountAsync(TestContext context)
    {
        var arn = RequireString(context, "IamPolicyArn");
        var response = await clients.IAM().GetPolicyAsync(new GetPolicyRequest { PolicyArn = arn });
        return response.Policy.AttachmentCount;
    }

    /// <summary>
    /// AttachmentCount moves 0 to 1. "The number of entities (users, groups,
    /// and roles) that the policy is attached to" (IAM API Reference,
    /// API_Policy.html) is what a cleanup script reads before deleting a
    /// policy, so a stuck 0 deletes something in use.
    /// </summary>
    private async Task GetPolicyAttachmentCountAfterAttachAsync(TestContext context)
    {
        const string op = "GetPolicyAttachmentCountAfterAttach";
        var arn = RequireString(context, "IamPolicyArn");
        var roleName = RequireString(context, "IamPolicyRoleName");
        Assertions.Equal<int?>(0, await AttachmentCountAsync(context), $"{op}: AttachmentCount before the attach");
        await clients.IAM().AttachRolePolicyAsync(new AttachRolePolicyRequest { RoleName = roleName, PolicyArn = arn });
        Assertions.Equal<int?>(1, await AttachmentCountAsync(context), $"{op}: AttachmentCount after attaching to one role");
    }

    /// <summary>The counter moves back to 0, which a never-decremented one would fail.</summary>
    private async Task GetPolicyAttachmentCountAfterDetachAsync(TestContext context)
    {
        const string op = "GetPolicyAttachmentCountAfterDetach";
        var arn = RequireString(context, "IamPolicyArn");
        var roleName = RequireString(context, "IamPolicyRoleName");
        await clients.IAM().DetachRolePolicyAsync(new DetachRolePolicyRequest { RoleName = roleName, PolicyArn = arn });
        Assertions.Equal<int?>(0, await AttachmentCountAsync(context), $"{op}: AttachmentCount after the detach");
    }

    private async Task DeletePolicyAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-del-policy";
        var create = await clients.IAM().CreatePolicyAsync(new CreatePolicyRequest
        {
            PolicyName = name,
            PolicyDocument = "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBucket\",\"Resource\":\"*\"}]}",
        });
        var arn = create.Policy.Arn;
        await clients.IAM().DeletePolicyAsync(new DeletePolicyRequest { PolicyArn = arn });
        var list = await clients.IAM().ListPoliciesAsync(new ListPoliciesRequest());
        Assertions.False(list.Policies.Any(p => p.Arn == arn), $"DeletePolicy: policy {arn} still present after deletion (runId={context.RunId})");
    }

    private async Task TeardownPoliciesAsync(TestContext context)
    {
        var arn = context.GetString("IamPolicyArn");
        var roleName = context.GetString("IamPolicyRoleName");
        if (!string.IsNullOrWhiteSpace(roleName))
        {
            if (!string.IsNullOrWhiteSpace(arn))
            {
                try { await clients.IAM().DetachRolePolicyAsync(new DetachRolePolicyRequest { RoleName = roleName, PolicyArn = arn }); } catch { }
            }

            try { await clients.IAM().DeleteRoleAsync(new DeleteRoleRequest { RoleName = roleName }); } catch { }
        }

        if (!string.IsNullOrWhiteSpace(arn))
        {
            try { await clients.IAM().DeletePolicyAsync(new DeletePolicyRequest { PolicyArn = arn }); } catch { }
        }
    }

    // ── iam-groups ──

    private async Task SetupGroupsAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-group";
        await clients.IAM().CreateGroupAsync(new CreateGroupRequest { GroupName = name });
        context.Set("IamGroupName", name);
    }

    private async Task CreateGroupAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-create-group";
        var response = await clients.IAM().CreateGroupAsync(new CreateGroupRequest { GroupName = name });
        Assertions.NotBlank(response.Group.GroupId, "CreateGroup: GroupId");
        try
        {
            var get = await clients.IAM().GetGroupAsync(new GetGroupRequest { GroupName = name });
            Assertions.Equal(name, get.Group.GroupName, "CreateGroup: GroupName mismatch");
        }
        finally
        {
            try { await clients.IAM().DeleteGroupAsync(new DeleteGroupRequest { GroupName = name }); } catch { }
        }
    }

    private async Task AddUserToGroupAsync(TestContext context)
    {
        var groupName = RequireString(context, "IamGroupName");
        var userName = $"{context.RunId}-iam-temp-user";
        await clients.IAM().CreateUserAsync(new CreateUserRequest { UserName = userName });
        context.Set("IamTempUserName", userName);
        await clients.IAM().AddUserToGroupAsync(new AddUserToGroupRequest { GroupName = groupName, UserName = userName });
    }

    private async Task ListGroupsForUserAsync(TestContext context)
    {
        var groupName = RequireString(context, "IamGroupName");
        var userName = RequireString(context, "IamTempUserName");
        var response = await clients.IAM().ListGroupsForUserAsync(new ListGroupsForUserRequest { UserName = userName });
        Assertions.True(response.Groups.Any(g => g.GroupName == groupName), $"ListGroupsForUser: group {groupName} not found for user (runId={context.RunId})");
    }

    private async Task RemoveUserFromGroupAsync(TestContext context)
    {
        var groupName = RequireString(context, "IamGroupName");
        var userName = RequireString(context, "IamTempUserName");
        await clients.IAM().RemoveUserFromGroupAsync(new RemoveUserFromGroupRequest { GroupName = groupName, UserName = userName });
    }

    private async Task GetGroupAsync(TestContext context)
    {
        var groupName = RequireString(context, "IamGroupName");
        var response = await clients.IAM().GetGroupAsync(new GetGroupRequest { GroupName = groupName });
        Assertions.Equal(groupName, response.Group.GroupName, "GetGroup: GroupName mismatch");
    }

    private async Task DeleteGroupAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-del-group";
        await clients.IAM().CreateGroupAsync(new CreateGroupRequest { GroupName = name });
        await clients.IAM().DeleteGroupAsync(new DeleteGroupRequest { GroupName = name });
        try
        {
            await clients.IAM().GetGroupAsync(new GetGroupRequest { GroupName = name });
            throw new InvalidOperationException($"DeleteGroup: group {name} still present after deletion (runId={context.RunId})");
        }
        catch (NoSuchEntityException)
        {
            // expected
        }
    }

    private async Task TeardownGroupsAsync(TestContext context)
    {
        var groupName = context.GetString("IamGroupName");
        if (string.IsNullOrWhiteSpace(groupName))
        {
            return;
        }

        var userName = context.GetString("IamTempUserName");
        if (!string.IsNullOrWhiteSpace(userName))
        {
            try { await clients.IAM().RemoveUserFromGroupAsync(new RemoveUserFromGroupRequest { GroupName = groupName, UserName = userName }); } catch { }
            try { await clients.IAM().DeleteUserAsync(new DeleteUserRequest { UserName = userName }); } catch { }
        }

        try { await clients.IAM().DeleteGroupAsync(new DeleteGroupRequest { GroupName = groupName }); } catch { }
    }

    private static string RequireString(TestContext context, string key)
    {
        return context.GetString(key) ?? throw new InvalidOperationException($"{key} not set");
    }

    // ── iam-simulate ──

    // SimPolicy is the identity policy the simulate group evaluates: read one
    // run-scoped prefix, nothing else.
    private static string SimPolicy(TestContext context) =>
        $@"{{""Version"":""2012-10-17"",""Statement"":[{{""Effect"":""Allow"",""Action"":""s3:GetObject"",""Resource"":""arn:aws:s3:::{context.RunId}-sim/*""}}]}}";

    private static string SimResource(TestContext context) =>
        $"arn:aws:s3:::{context.RunId}-sim/report.csv";

    private async Task SetupSimulateAsync(TestContext context)
    {
        var name = $"{context.RunId}-iam-sim-user";
        await clients.IAM().CreateUserAsync(new CreateUserRequest { UserName = name });
        await clients.IAM().PutUserPolicyAsync(new PutUserPolicyRequest
        {
            UserName = name,
            PolicyName = "sim-allow-read",
            PolicyDocument = SimPolicy(context),
        });
        context.Set("IamSimUserName", name);
    }

    private async Task TeardownSimulateAsync(TestContext context)
    {
        var name = context.GetString("IamSimUserName");
        if (string.IsNullOrEmpty(name)) return;
        try { await clients.IAM().DeleteUserPolicyAsync(new DeleteUserPolicyRequest { UserName = name, PolicyName = "sim-allow-read" }); } catch { }
        try { await clients.IAM().DeleteUserAsync(new DeleteUserRequest { UserName = name }); } catch { }
    }

    private async Task SimulateCustomPolicyAllowedAsync(TestContext context)
    {
        var response = await clients.IAM().SimulateCustomPolicyAsync(new SimulateCustomPolicyRequest
        {
            PolicyInputList = [SimPolicy(context)],
            ActionNames = ["s3:GetObject"],
            ResourceArns = [SimResource(context)],
        });
        Assertions.True(response.EvaluationResults.Count > 0, "SimulateCustomPolicy: no EvaluationResults");
        Assertions.Equal("allowed", response.EvaluationResults[0].EvalDecision.Value, "SimulateCustomPolicy: EvalDecision");
        Assertions.Equal("s3:GetObject", response.EvaluationResults[0].EvalActionName, "SimulateCustomPolicy: EvalActionName");
    }

    private async Task SimulateCustomPolicyImplicitDenyAsync(TestContext context)
    {
        var response = await clients.IAM().SimulateCustomPolicyAsync(new SimulateCustomPolicyRequest
        {
            PolicyInputList = [SimPolicy(context)],
            ActionNames = ["s3:PutObject"],
            ResourceArns = [SimResource(context)],
        });
        Assertions.True(response.EvaluationResults.Count > 0, "SimulateCustomPolicy: no EvaluationResults");
        Assertions.Equal("implicitDeny", response.EvaluationResults[0].EvalDecision.Value, "SimulateCustomPolicy: EvalDecision");
    }

    private async Task SimulateCustomPolicyExplicitDenyAsync(TestContext context)
    {
        const string doc = @"{""Version"":""2012-10-17"",""Statement"":[" +
            @"{""Effect"":""Allow"",""Action"":""s3:*"",""Resource"":""*""}," +
            @"{""Effect"":""Deny"",""Action"":""s3:DeleteObject"",""Resource"":""*""}]}";
        var response = await clients.IAM().SimulateCustomPolicyAsync(new SimulateCustomPolicyRequest
        {
            PolicyInputList = [doc],
            ActionNames = ["s3:DeleteObject"],
            ResourceArns = [SimResource(context)],
        });
        Assertions.True(response.EvaluationResults.Count > 0, "SimulateCustomPolicy: no EvaluationResults");
        Assertions.Equal("explicitDeny", response.EvaluationResults[0].EvalDecision.Value, "SimulateCustomPolicy: EvalDecision");
    }

    private async Task SimulatePrincipalPolicyAllowedAsync(TestContext context)
    {
        var userName = RequireString(context, "IamSimUserName");
        var response = await clients.IAM().SimulatePrincipalPolicyAsync(new SimulatePrincipalPolicyRequest
        {
            PolicySourceArn = $"arn:aws:iam::000000000000:user/{userName}",
            ActionNames = ["s3:GetObject"],
            ResourceArns = [SimResource(context)],
        });
        Assertions.True(response.EvaluationResults.Count > 0, "SimulatePrincipalPolicy: no EvaluationResults");
        Assertions.Equal("allowed", response.EvaluationResults[0].EvalDecision.Value, "SimulatePrincipalPolicy: EvalDecision");
        Assertions.True(
            response.EvaluationResults[0].MatchedStatements.Any(s => s.SourcePolicyId == "sim-allow-read"),
            "SimulatePrincipalPolicy: MatchedStatements missing sim-allow-read");
    }

    private async Task SimulatePrincipalPolicyImplicitDenyAsync(TestContext context)
    {
        var userName = RequireString(context, "IamSimUserName");
        var response = await clients.IAM().SimulatePrincipalPolicyAsync(new SimulatePrincipalPolicyRequest
        {
            PolicySourceArn = $"arn:aws:iam::000000000000:user/{userName}",
            ActionNames = ["s3:DeleteObject"],
            ResourceArns = [SimResource(context)],
        });
        Assertions.True(response.EvaluationResults.Count > 0, "SimulatePrincipalPolicy: no EvaluationResults");
        Assertions.Equal("implicitDeny", response.EvaluationResults[0].EvalDecision.Value, "SimulatePrincipalPolicy: EvalDecision");
    }

}
