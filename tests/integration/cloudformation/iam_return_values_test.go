package cloudformation_test

// What `Ref` and `Fn::GetAtt` return for the four IAM resource types a CDK app
// provisions (issue #154). These are the strings a template hands on to
// something else — an EC2 launch template's IamInstanceProfile.Name, a policy's
// Roles list, a stack Output another stack imports — so a wrong one is not a
// cosmetic difference; it is an ARN arriving where AWS documents a name.
//
// Sources, per resource:
//   AWS::IAM::Role            Ref = role name; GetAtt Arn, RoleId
//   https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-role.html
//   AWS::IAM::InstanceProfile Ref = instance profile name; GetAtt Arn
//   https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-instanceprofile.html
//   AWS::IAM::ManagedPolicy   Ref = the policy ARN
//   https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-managedpolicy.html
//   AWS::IAM::Policy          Ref = the resource name; at least one of
//                             Groups/Roles/Users is required
//   https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-iam-policy.html

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// iamReturnValuesTemplate is the "Role with Embedded Policy and Instance
// Profiles" shape from the AWS::IAM::Role documentation, with every return
// value exported so a single stack pins them all.
const iamReturnValuesTemplate = `{
  "Resources": {
    "RootRole": {
      "Type": "AWS::IAM::Role",
      "Properties": {
        "RoleName": "cfn-return-role",
        "AssumeRolePolicyDocument": {
          "Version": "2012-10-17",
          "Statement": [{
            "Effect": "Allow",
            "Principal": {"Service": ["ec2.amazonaws.com"]},
            "Action": ["sts:AssumeRole"]
          }]
        },
        "Path": "/"
      }
    },
    "RootManagedPolicy": {
      "Type": "AWS::IAM::ManagedPolicy",
      "Properties": {
        "ManagedPolicyName": "cfn-return-managed",
        "PolicyDocument": {
          "Version": "2012-10-17",
          "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "*"}]
        },
        "Roles": [{"Ref": "RootRole"}]
      }
    },
    "RootInlinePolicy": {
      "Type": "AWS::IAM::Policy",
      "Properties": {
        "PolicyName": "cfn-return-inline",
        "PolicyDocument": {
          "Version": "2012-10-17",
          "Statement": [{"Effect": "Allow", "Action": "sqs:SendMessage", "Resource": "*"}]
        },
        "Roles": [{"Ref": "RootRole"}]
      }
    },
    "RootInstanceProfile": {
      "Type": "AWS::IAM::InstanceProfile",
      "Properties": {
        "InstanceProfileName": "cfn-return-profile",
        "Path": "/",
        "Roles": [{"Ref": "RootRole"}]
      }
    }
  },
  "Outputs": {
    "RoleRef":       {"Value": {"Ref": "RootRole"}},
    "RoleArn":       {"Value": {"Fn::GetAtt": ["RootRole", "Arn"]}},
    "RoleId":        {"Value": {"Fn::GetAtt": ["RootRole", "RoleId"]}},
    "ManagedRef":    {"Value": {"Ref": "RootManagedPolicy"}},
    "ProfileRef":    {"Value": {"Ref": "RootInstanceProfile"}},
    "ProfileArn":    {"Value": {"Fn::GetAtt": ["RootInstanceProfile", "Arn"]}}
  }
}`

// TestCreateStack_IAMReturnValues pins Ref and GetAtt for all four resource
// types in one deploy. The instance profile's Ref is the one that was wrong:
// it returned the ARN, so a template feeding it to a property AWS documents as
// a name — AWS::EC2::Instance's IamInstanceProfile, an EC2 launch template's
// IamInstanceProfile.Name — passed an ARN there.
func TestCreateStack_IAMReturnValues(t *testing.T) {
	// Given: a stack with a role, a managed policy, an inline policy and a profile
	srv := helpers.NewTestServer(t)
	const stackName = "iam-return-values"
	createIAMStack(t, srv, stackName, iamReturnValuesTemplate)

	// Then: each return value has the documented shape
	for _, tc := range []struct {
		output string
		want   string
	}{
		{"RoleRef", "cfn-return-role"},
		{"RoleArn", "arn:aws:iam::000000000000:role/cfn-return-role"},
		{"ManagedRef", "arn:aws:iam::000000000000:policy/cfn-return-managed"},
		{"ProfileRef", "cfn-return-profile"},
		{"ProfileArn", "arn:aws:iam::000000000000:instance-profile/cfn-return-profile"},
	} {
		if got := stackOutput(t, srv, stackName, tc.output); got != tc.want {
			t.Errorf("output %s = %q, want %q", tc.output, got, tc.want)
		}
	}

	// And: RoleId is IAM's own role ID, not a name or an ARN
	if roleID := stackOutput(t, srv, stackName, "RoleId"); !strings.HasPrefix(roleID, "AROA") {
		t.Errorf("output RoleId = %q, want an AROA-prefixed role ID", roleID)
	}

	// And: the profile's Ref is usable as the InstanceProfileName it claims to be
	assertIAMSuccess(t, srv, "GetInstanceProfile", url.Values{
		"InstanceProfileName": {stackOutput(t, srv, stackName, "ProfileRef")},
	})
}

// TestDeleteStack_IAMReturnValuesStackTearsDown proves the same stack unwinds:
// the instance profile has to give up its role before IAM will delete it, and
// the role has to lose both policies before IAM will delete the role.
func TestDeleteStack_IAMReturnValuesStackTearsDown(t *testing.T) {
	// Given: the stack from TestCreateStack_IAMReturnValues
	srv := helpers.NewTestServer(t)
	const stackName = "iam-return-values-teardown"
	createIAMStack(t, srv, stackName, iamReturnValuesTemplate)

	// When: the stack is deleted
	resp := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if status := waitForStackStatusIn(t, srv, stackName, "DELETE_COMPLETE", "DELETE_FAILED"); status != "DELETE_COMPLETE" {
		events := cfnQuery(t, srv, "DescribeStackEvents", url.Values{"StackName": {stackName}})
		defer events.Body.Close()
		t.Fatalf("delete stack ended in %s: %s", status, helpers.ReadBody(t, events))
	}

	// Then: every IAM entity it created is gone
	for _, tc := range []struct {
		action string
		params url.Values
	}{
		{"GetRole", url.Values{"RoleName": {"cfn-return-role"}}},
		{"GetInstanceProfile", url.Values{"InstanceProfileName": {"cfn-return-profile"}}},
		{"GetPolicy", url.Values{"PolicyArn": {"arn:aws:iam::000000000000:policy/cfn-return-managed"}}},
	} {
		got := iamQuery(t, srv, tc.action, tc.params)
		got.Body.Close()
		helpers.AssertStatus(t, got, http.StatusNotFound)
	}
}

// iamPathedRoleTemplate is a role at a non-default Path whose ARN and the
// profile that holds it are both exported, with `description` varying so an
// update has something to change.
func iamPathedRoleTemplate(description string) string {
	return `{
  "Resources": {
    "PathedRole": {
      "Type": "AWS::IAM::Role",
      "Properties": {
        "RoleName": "cfn-pathed-role",
        "Path": "/service-role/",
        "Description": "` + description + `",
        "AssumeRolePolicyDocument": {
          "Version": "2012-10-17",
          "Statement": [{
            "Effect": "Allow",
            "Principal": {"Service": ["ec2.amazonaws.com"]},
            "Action": ["sts:AssumeRole"]
          }]
        }
      }
    },
    "PathedProfile": {
      "Type": "AWS::IAM::InstanceProfile",
      "Properties": {
        "InstanceProfileName": "cfn-pathed-profile",
        "Path": "/service-role/",
        "Roles": [{"Ref": "PathedRole"}]
      }
    }
  },
  "Outputs": {
    "RoleArn":    {"Value": {"Fn::GetAtt": ["PathedRole", "Arn"]}},
    "ProfileRef": {"Value": {"Ref": "PathedProfile"}},
    "ProfileArn": {"Value": {"Fn::GetAtt": ["PathedProfile", "Arn"]}}
  }
}`
}

// TestUpdateStack_IAMPathedEntities_keepThePathInTheirARNs pins GetAtt "Arn"
// across an update for entities at a non-default Path. Path is create-only on
// both types, so the ARN an update reports has to be the one Create returned —
// the update handlers restated it without the path, so a role at
// /service-role/ (CDK's default for a service role) changed ARN shape on any
// unrelated property change.
func TestUpdateStack_IAMPathedEntities_keepThePathInTheirARNs(t *testing.T) {
	// Given: a stack with a role and instance profile under /service-role/
	srv := helpers.NewTestServer(t)
	const stackName = "iam-pathed-arns"
	createIAMStack(t, srv, stackName, iamPathedRoleTemplate("before"))

	const (
		wantRoleArn    = "arn:aws:iam::000000000000:role/service-role/cfn-pathed-role"
		wantProfileArn = "arn:aws:iam::000000000000:instance-profile/service-role/cfn-pathed-profile"
	)
	if got := stackOutput(t, srv, stackName, "RoleArn"); got != wantRoleArn {
		t.Fatalf("RoleArn after create = %q, want %q", got, wantRoleArn)
	}
	if got := stackOutput(t, srv, stackName, "ProfileArn"); got != wantProfileArn {
		t.Fatalf("ProfileArn after create = %q, want %q", got, wantProfileArn)
	}

	// When: an unrelated property changes
	updateIAMStack(t, srv, stackName, iamPathedRoleTemplate("after"))

	// Then: both ARNs still carry the path, and the profile's Ref is still its name
	if got := stackOutput(t, srv, stackName, "RoleArn"); got != wantRoleArn {
		t.Errorf("RoleArn after update = %q, want %q", got, wantRoleArn)
	}
	if got := stackOutput(t, srv, stackName, "ProfileArn"); got != wantProfileArn {
		t.Errorf("ProfileArn after update = %q, want %q", got, wantProfileArn)
	}
	if got := stackOutput(t, srv, stackName, "ProfileRef"); got != "cfn-pathed-profile" {
		t.Errorf("ProfileRef after update = %q, want cfn-pathed-profile", got)
	}
}

// TestUpdateStack_IAMInstanceProfile_roleSwap covers the sequence AWS's own
// guidance prescribes for changing an instance profile's role — "remove the
// existing role and then add a different role" — which the quota of one role
// per profile makes the only order that works.
func TestUpdateStack_IAMInstanceProfile_roleSwap(t *testing.T) {
	// Given: a profile holding the first of two roles
	srv := helpers.NewTestServer(t)
	const stackName = "iam-profile-role-swap"
	template := func(role string) string {
		return `{
  "Resources": {
    "FirstRole":  ` + iamSwapRoleResource("cfn-swap-first") + `,
    "SecondRole": ` + iamSwapRoleResource("cfn-swap-second") + `,
    "Profile": {
      "Type": "AWS::IAM::InstanceProfile",
      "Properties": {
        "InstanceProfileName": "cfn-swap-profile",
        "Roles": ["` + role + `"]
      },
      "DependsOn": ["FirstRole", "SecondRole"]
    }
  }
}`
	}
	createIAMStack(t, srv, stackName, template("cfn-swap-first"))
	assertInstanceProfileRoles(t, srv, "cfn-swap-profile", "cfn-swap-first")

	// When: the template names the other role
	updateIAMStack(t, srv, stackName, template("cfn-swap-second"))

	// Then: the profile holds exactly the replacement
	assertInstanceProfileRoles(t, srv, "cfn-swap-profile", "cfn-swap-second")
}

func iamSwapRoleResource(name string) string {
	return `{
      "Type": "AWS::IAM::Role",
      "Properties": {
        "RoleName": "` + name + `",
        "AssumeRolePolicyDocument": ` + cfnTrustPolicy + `
      }
    }`
}

// assertInstanceProfileRoles checks GetInstanceProfile reports exactly want.
func assertInstanceProfileRoles(t *testing.T, srv *helpers.TestServer, profile string, want ...string) {
	t.Helper()
	resp := iamQuery(t, srv, "GetInstanceProfile", url.Values{"InstanceProfileName": {profile}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var decoded struct {
		Roles []string `xml:"GetInstanceProfileResult>InstanceProfile>Roles>member>RoleName"`
	}
	helpers.DecodeXML(t, resp, &decoded)
	if strings.Join(decoded.Roles, ",") != strings.Join(want, ",") {
		t.Errorf("instance profile %s roles = %v, want %v", profile, decoded.Roles, want)
	}
}

// TestCreateStack_IAMPolicyWithoutPrincipals fails the stack rather than
// creating a resource that writes nothing.
//
// "The Groups, Roles, and Users properties are optional. However, you must
// specify at least one of these properties." An AWS::IAM::Policy with none of
// them named is a template CloudFormation refuses; Overcast used to report
// CREATE_COMPLETE for a resource whose policy document had reached no
// principal at all, so a missing `Roles:` line showed up later as a permission
// that silently did not exist.
func TestCreateStack_IAMPolicyWithoutPrincipals(t *testing.T) {
	// Given: an inline policy naming no principal
	srv := helpers.NewTestServer(t)
	const stackName = "iam-policy-no-principals"
	template := `{
  "Resources": {
    "Orphan": {
      "Type": "AWS::IAM::Policy",
      "Properties": {
        "PolicyName": "orphan",
        "PolicyDocument": {
          "Version": "2012-10-17",
          "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "*"}]
        }
      }
    }
  }
}`

	// When: the stack is created
	resp := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: it rolls back rather than reporting success
	status := waitForStackStatusIn(t, srv, stackName, "CREATE_COMPLETE", "ROLLBACK_COMPLETE", "ROLLBACK_FAILED")
	if status == "CREATE_COMPLETE" {
		t.Fatal("stack reported CREATE_COMPLETE for an AWS::IAM::Policy with no Groups, Roles or Users")
	}

	// And: the reason names the properties the template has to pick from
	events := cfnQuery(t, srv, "DescribeStackEvents", url.Values{"StackName": {stackName}})
	defer events.Body.Close()
	body := helpers.ReadBody(t, events)
	for _, want := range []string{"Groups", "Roles", "Users"} {
		if !strings.Contains(body, want) {
			t.Errorf("failure reason does not name %s: %s", want, body)
		}
	}
}
