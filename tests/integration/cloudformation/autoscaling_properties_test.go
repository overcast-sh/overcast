package cloudformation_test

// autoscaling_properties_test.go — AWS::AutoScaling::AutoScalingGroup and
// AWS::AutoScaling::LaunchConfiguration property threading (#2060, the #540
// tail pass).
//
// AutoScalingGroupHandler.Create sent only MinSize, MaxSize, DesiredCapacity,
// AvailabilityZones, VPCZoneIdentifier, the launch source and Tags —
// Cooldown, HealthCheckType, HealthCheckGracePeriod and TerminationPolicies
// were dropped outright, on both Create and Update.
// LaunchConfigurationHandler.Create sent only ImageId, InstanceType and
// SecurityGroups — KeyName, IamInstanceProfile and UserData were dropped.
//
// These read back through DescribeAutoScalingGroups and
// DescribeLaunchConfigurations, the service's own Describe API, rather than
// asserting on the request the handler built — see eks_properties_test.go's
// header for why. UserData is the one exception: it reaches
// CreateLaunchConfiguration and is stored, but DescribeLaunchConfigurations
// does not echo it back (a pre-existing AutoScaling service gap, outside this
// package's fence — see the comment on autoscalingLaunchConfigCreateProperties
// in internal/services/cloudformation/provisioner_query_rest_coverage.go), so
// it is not asserted here.

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// autoscalingPropertiesTemplate sets every AWS::AutoScaling::AutoScalingGroup
// and AWS::AutoScaling::LaunchConfiguration property the handlers forward,
// plus one property on each resource they do not (NewInstancesProtectedFromScaleIn,
// SpotPrice) to pin the unconsumed-property notice.
const autoscalingPropertiesTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "LaunchConfig": {
      "Type": "AWS::AutoScaling::LaunchConfiguration",
      "Properties": {
        "LaunchConfigurationName": "cfn-props-lc",
        "ImageId": "ami-0123456789abcdef0",
        "InstanceType": "t3.micro",
        "KeyName": "cfn-props-key",
        "IamInstanceProfile": "cfn-props-profile",
        "UserData": "IyEvYmluL2Jhc2gKZWNobyBoaQ==",
        "SpotPrice": "0.01"
      }
    },
    "ASG": {
      "Type": "AWS::AutoScaling::AutoScalingGroup",
      "Properties": {
        "AutoScalingGroupName": "cfn-props-asg",
        "MinSize": "1",
        "MaxSize": "3",
        "DesiredCapacity": "2",
        "AvailabilityZones": ["us-east-1a"],
        "LaunchConfigurationName": {"Ref": "LaunchConfig"},
        "Cooldown": "120",
        "HealthCheckType": "ELB",
        "HealthCheckGracePeriod": 90,
        "TerminationPolicies": ["OldestInstance", "Default"],
        "NewInstancesProtectedFromScaleIn": true
      }
    }
  }
}`

// asgDescribeResult is the subset of DescribeAutoScalingGroups this test
// reads back.
type asgDescribeResult struct {
	Groups []struct {
		DefaultCooldown         int      `xml:"DefaultCooldown"`
		HealthCheckType         string   `xml:"HealthCheckType"`
		HealthCheckGracePeriod  int      `xml:"HealthCheckGracePeriod"`
		TerminationPolicies     []string `xml:"TerminationPolicies>member"`
		LaunchConfigurationName string   `xml:"LaunchConfigurationName"`
	} `xml:"DescribeAutoScalingGroupsResult>AutoScalingGroups>member"`
}

func asgDescribeGroup(t *testing.T, srv *helpers.TestServer, name string) asgDescribeResult {
	t.Helper()
	resp := asQuery(t, srv, "DescribeAutoScalingGroups", url.Values{
		"AutoScalingGroupNames.member.1": {name},
	})
	defer resp.Body.Close()
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DescribeAutoScalingGroups: status %d: %s", resp.StatusCode, body)
	}
	var out asgDescribeResult
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode DescribeAutoScalingGroupsResponse: %v\n%s", err, body)
	}
	return out
}

// asgDescribeLaunchConfigResult is the subset of
// DescribeLaunchConfigurations this test reads back.
type asgDescribeLaunchConfigResult struct {
	Configs []struct {
		KeyName            string `xml:"KeyName"`
		IamInstanceProfile string `xml:"IamInstanceProfile"`
	} `xml:"DescribeLaunchConfigurationsResult>LaunchConfigurations>member"`
}

func asgDescribeLaunchConfig(t *testing.T, srv *helpers.TestServer, name string) asgDescribeLaunchConfigResult {
	t.Helper()
	resp := asQuery(t, srv, "DescribeLaunchConfigurations", url.Values{
		"LaunchConfigurationNames.member.1": {name},
	})
	defer resp.Body.Close()
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DescribeLaunchConfigurations: status %d: %s", resp.StatusCode, body)
	}
	var out asgDescribeLaunchConfigResult
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode DescribeLaunchConfigurationsResponse: %v\n%s", err, body)
	}
	return out
}

// TestCreateStack_AutoScaling_propertiesThreaded is the failing-first case
// for the AutoScaling half of #2060. Before the fix, Cooldown,
// HealthCheckType, HealthCheckGracePeriod and TerminationPolicies were
// dropped from AutoScalingGroup, and KeyName and IamInstanceProfile were
// dropped from LaunchConfiguration.
func TestCreateStack_AutoScaling_propertiesThreaded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "autoscaling-properties-stack"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {autoscalingPropertiesTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	groups := asgDescribeGroup(t, srv, "cfn-props-asg")
	if len(groups.Groups) != 1 {
		t.Fatalf("DescribeAutoScalingGroups returned %d groups, want 1", len(groups.Groups))
	}
	group := groups.Groups[0]

	if group.DefaultCooldown != 120 {
		t.Errorf("DefaultCooldown = %d, want 120 (from the template's Cooldown)", group.DefaultCooldown)
	}
	if group.HealthCheckType != "ELB" {
		t.Errorf("HealthCheckType = %q, want ELB", group.HealthCheckType)
	}
	if group.HealthCheckGracePeriod != 90 {
		t.Errorf("HealthCheckGracePeriod = %d, want 90", group.HealthCheckGracePeriod)
	}
	wantPolicies := []string{"OldestInstance", "Default"}
	if len(group.TerminationPolicies) != len(wantPolicies) {
		t.Fatalf("TerminationPolicies = %v, want %v", group.TerminationPolicies, wantPolicies)
	}
	for i, want := range wantPolicies {
		if group.TerminationPolicies[i] != want {
			t.Errorf("TerminationPolicies[%d] = %q, want %q", i, group.TerminationPolicies[i], want)
		}
	}

	configs := asgDescribeLaunchConfig(t, srv, "cfn-props-lc")
	if len(configs.Configs) != 1 {
		t.Fatalf("DescribeLaunchConfigurations returned %d configs, want 1", len(configs.Configs))
	}
	lc := configs.Configs[0]
	if lc.KeyName != "cfn-props-key" {
		t.Errorf("KeyName = %q, want cfn-props-key", lc.KeyName)
	}
	if lc.IamInstanceProfile != "cfn-props-profile" {
		t.Errorf("IamInstanceProfile = %q, want cfn-props-profile", lc.IamInstanceProfile)
	}

	// And the properties the handlers do not act on are reported rather than
	// dropped in silence — see noteUnconsumedProperties. Both resources'
	// reasons land on the same DescribeStackResources response.
	reasons := describeStackResourceReasons(t, srv, stackName)
	if !strings.Contains(reasons, "NewInstancesProtectedFromScaleIn") {
		t.Errorf("expected the AutoScalingGroup's ResourceStatusReason to name the unapplied NewInstancesProtectedFromScaleIn, got: %s", reasons)
	}
	if !strings.Contains(reasons, "SpotPrice") {
		t.Errorf("expected the LaunchConfiguration's ResourceStatusReason to name the unapplied SpotPrice, got: %s", reasons)
	}
}

// TestUpdateStack_AutoScalingGroup_cooldownAndHealthCheckThreaded is the
// failing-first case for Update: the same four properties were dropped there
// too.
func TestUpdateStack_AutoScalingGroup_cooldownAndHealthCheckThreaded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "autoscaling-update-properties-stack"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {autoscalingPropertiesTemplate},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	updateTemplate := strings.NewReplacer(
		`"Cooldown": "120"`, `"Cooldown": "300"`,
		`"HealthCheckType": "ELB"`, `"HealthCheckType": "EC2"`,
		`"HealthCheckGracePeriod": 90`, `"HealthCheckGracePeriod": 45`,
		`["OldestInstance", "Default"]`, `["NewestInstance"]`,
	).Replace(autoscalingPropertiesTemplate)

	update := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {updateTemplate},
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	group := asgDescribeGroup(t, srv, "cfn-props-asg").Groups[0]
	if group.DefaultCooldown != 300 {
		t.Errorf("DefaultCooldown after update = %d, want 300", group.DefaultCooldown)
	}
	if group.HealthCheckType != "EC2" {
		t.Errorf("HealthCheckType after update = %q, want EC2", group.HealthCheckType)
	}
	if group.HealthCheckGracePeriod != 45 {
		t.Errorf("HealthCheckGracePeriod after update = %d, want 45", group.HealthCheckGracePeriod)
	}
	if len(group.TerminationPolicies) != 1 || group.TerminationPolicies[0] != "NewestInstance" {
		t.Errorf("TerminationPolicies after update = %v, want [NewestInstance]", group.TerminationPolicies)
	}
}
