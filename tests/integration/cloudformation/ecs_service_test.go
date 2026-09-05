package cloudformation_test

// ecs_service_test.go — AWS::ECS::Service provisioning. Shares cfnQuery,
// waitForStackStatus and the rest of the harness with cloudformation_test.go.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// fargateServiceTemplate is the shape CDK emits for a Fargate service: the
// service names no DesiredCount, because since v2 the construct omits the
// property unless the app sets one and relies on CloudFormation's default.
const fargateServiceTemplate = `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Cluster": {
      "Type": "AWS::ECS::Cluster",
      "Properties": { "ClusterName": "cfn-svc-cluster" }
    },
    "TaskDefinition": {
      "Type": "AWS::ECS::TaskDefinition",
      "Properties": {
        "Family": "cfn-svc-task",
        "Cpu": "256",
        "Memory": "512",
        "NetworkMode": "awsvpc",
        "RequiresCompatibilities": ["FARGATE"],
        "ContainerDefinitions": [{ "Name": "app", "Image": "nginx:latest" }]
      }
    },
    "Service": {
      "Type": "AWS::ECS::Service",
      "Properties": {
        "ServiceName": "cfn-svc",
        "Cluster": { "Ref": "Cluster" },
        "TaskDefinition": { "Ref": "TaskDefinition" },
        "LaunchType": "FARGATE",
        "NetworkConfiguration": {
          "AwsvpcConfiguration": {
            "Subnets": ["subnet-12345678"],
            "AssignPublicIp": "DISABLED"
          }
        }
      }
    }
  }
}`

// TestCreateStack_ECSServiceDefaultsDesiredCount pins CloudFormation's
// documented default: "For new services, if a desired count is not specified, a
// default value of 1 is used." Without it the service is created at the ECS
// API's own default of zero and sits ACTIVE at 0/0, having started nothing —
// which is what a CDK-deployed Fargate service did.
func TestCreateStack_ECSServiceDefaultsDesiredCount(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	srv := helpers.NewTestServer(t)

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"ecs-service-stack"},
		"TemplateBody": []string{fargateServiceTemplate},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)

	waitForStackStatus(t, srv, "ecs-service-stack", "CREATE_COMPLETE")

	// Then: the service wants one task and is running it
	resp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "DescribeServices",
		"application/x-amz-json-1.1", map[string]any{
			"cluster":  "cfn-svc-cluster",
			"services": []string{"cfn-svc"},
		})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var described struct {
		Services []struct {
			ServiceName  string `json:"serviceName"`
			DesiredCount int    `json:"desiredCount"`
			RunningCount int    `json:"runningCount"`
			Deployments  []struct {
				RolloutState string `json:"rolloutState"`
			} `json:"deployments"`
		} `json:"services"`
	}
	helpers.DecodeJSON(t, resp, &described)

	if len(described.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(described.Services))
	}
	svc := described.Services[0]
	if svc.DesiredCount != 1 {
		t.Errorf("expected desiredCount=1 (CloudFormation's default for a new service), got %d", svc.DesiredCount)
	}
	if svc.RunningCount != 1 {
		t.Errorf("expected runningCount=1, got %d", svc.RunningCount)
	}
	if len(svc.Deployments) == 0 || svc.Deployments[0].RolloutState != "COMPLETED" {
		t.Errorf("expected a COMPLETED rollout, got %#v", svc.Deployments)
	}
}

// TestCreateStack_ECSServiceExplicitDesiredCountWins checks the default does
// not override a count the template actually gave.
func TestCreateStack_ECSServiceExplicitDesiredCountWins(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	srv := helpers.NewTestServer(t)

	template := strings.Replace(fargateServiceTemplate,
		`"LaunchType": "FARGATE",`,
		`"LaunchType": "FARGATE", "DesiredCount": 3,`, 1)

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"ecs-service-count-stack"},
		"TemplateBody": []string{template},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)

	waitForStackStatus(t, srv, "ecs-service-count-stack", "CREATE_COMPLETE")

	resp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "DescribeServices",
		"application/x-amz-json-1.1", map[string]any{
			"cluster":  "cfn-svc-cluster",
			"services": []string{"cfn-svc"},
		})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var described struct {
		Services []struct {
			DesiredCount int `json:"desiredCount"`
			RunningCount int `json:"runningCount"`
		} `json:"services"`
	}
	helpers.DecodeJSON(t, resp, &described)
	if len(described.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(described.Services))
	}
	if described.Services[0].DesiredCount != 3 {
		t.Errorf("expected desiredCount=3 from the template, got %d", described.Services[0].DesiredCount)
	}
	if described.Services[0].RunningCount != 3 {
		t.Errorf("expected runningCount=3, got %d", described.Services[0].RunningCount)
	}
}

// TestCreateStack_ECSServiceMissingNetworkConfigurationFailsStack checks that a
// service CloudFormation cannot create fails the stack, rather than the stack
// reaching CREATE_COMPLETE around a service that never starts anything.
func TestCreateStack_ECSServiceMissingNetworkConfigurationFailsStack(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	srv := helpers.NewTestServer(t)

	template := strings.Replace(fargateServiceTemplate,
		`"NetworkConfiguration": {
          "AwsvpcConfiguration": {
            "Subnets": ["subnet-12345678"],
            "AssignPublicIp": "DISABLED"
          }
        }`, `"PlatformVersion": "LATEST"`, 1)

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"ecs-service-broken-stack"},
		"TemplateBody": []string{template},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)

	// Then: the stack fails rather than completing around a dead service
	status := waitForStackStatusIn(t, srv, "ecs-service-broken-stack",
		"CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS", "CREATE_COMPLETE")
	if status == "CREATE_COMPLETE" {
		t.Fatal("stack reached CREATE_COMPLETE with a service that cannot place tasks")
	}
}

// waitForStackStatusIn polls until the stack reports one of want, returning it.
func waitForStackStatusIn(t *testing.T, srv *helpers.TestServer, stackName string, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp := cfnQuery(t, srv, "DescribeStacks", url.Values{"StackName": []string{stackName}})
		status := resp.StatusCode
		body := string(readBody(t, resp))
		resp.Body.Close()
		for _, w := range want {
			if strings.Contains(body, "<StackStatus>"+w+"</StackStatus>") {
				return w
			}
			// A name-based DescribeStacks on a DELETE_COMPLETE stack now
			// answers "does not exist" instead of the status (#829, matching
			// AWS) — the does-not-exist ValidationError is what the real
			// stack-delete-complete SDK waiter treats as terminal success
			// alongside the literal status, so it counts as reaching
			// DELETE_COMPLETE here too.
			if w == "DELETE_COMPLETE" && status == http.StatusBadRequest && strings.Contains(body, "does not exist") {
				return w
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("stack %s never reached any of %v:\n%s", stackName, want, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
