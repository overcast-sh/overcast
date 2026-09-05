package cloudformation_test

// ecs_service_update_test.go — updating an AWS::ECS::Service, and what happens
// when the new deployment cannot run.
//
// An update that swaps in a task definition whose tasks fail is the most common
// way a real deployment goes wrong. CloudFormation has to notice, fail the
// resource and unwind — leaving the stack in a state the next `cdk deploy` can
// proceed from, rather than wedged.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// serviceStackTemplate renders the WordPress-shaped stack at a given desired
// count, so an update can change something the ECS service actually acts on.
func serviceStackTemplate(desiredCount string) string {
	return serviceStackTemplateWithImage(desiredCount, "nginx:latest")
}

// serviceStackTemplateWithImage renders the same stack on a chosen container
// image. Changing the image replaces the task definition, which is the update
// that makes the service roll out a new deployment — the shape every real
// `cdk deploy` of a code change takes.
func serviceStackTemplateWithImage(desiredCount, image string) string {
	return `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Cluster": {
      "Type": "AWS::ECS::Cluster",
      "Properties": { "ClusterName": "upd-cluster" }
    },
    "TaskDefinition": {
      "Type": "AWS::ECS::TaskDefinition",
      "Properties": {
        "Family": "upd-task",
        "Cpu": "256",
        "Memory": "512",
        "NetworkMode": "awsvpc",
        "RequiresCompatibilities": ["FARGATE"],
        "ContainerDefinitions": [{ "Name": "app", "Image": "` + image + `" }]
      }
    },
    "Service": {
      "Type": "AWS::ECS::Service",
      "Properties": {
        "ServiceName": "upd-svc",
        "Cluster": { "Ref": "Cluster" },
        "TaskDefinition": { "Ref": "TaskDefinition" },
        "LaunchType": "FARGATE",
        "DesiredCount": ` + desiredCount + `,
        "NetworkConfiguration": {
          "AwsvpcConfiguration": { "Subnets": ["subnet-12345678"] }
        }
      }
    }
  }
}`
}

func TestUpdateStack_ECSServiceDesiredCountChange(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	// Given: a deployed stack with a service running one task.
	srv := helpers.NewTestServer(t)
	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"ecs-upd-stack"},
		"TemplateBody": []string{serviceStackTemplate("1")},
	})
	cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "ecs-upd-stack", "CREATE_COMPLETE")

	// When: the stack is updated to want two.
	ur := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    []string{"ecs-upd-stack"},
		"TemplateBody": []string{serviceStackTemplate("2")},
	})
	ur.Body.Close()
	helpers.AssertStatus(t, ur, http.StatusOK)
	waitForStackStatus(t, srv, "ecs-upd-stack", "UPDATE_COMPLETE")

	// Then: the update waited for the new count rather than reporting success
	// with the service still catching up.
	resp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "DescribeServices",
		"application/x-amz-json-1.1", map[string]any{
			"cluster": "upd-cluster", "services": []string{"upd-svc"},
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
	if described.Services[0].DesiredCount != 2 || described.Services[0].RunningCount != 2 {
		t.Errorf("expected 2/2 after the update, got %d/%d",
			described.Services[0].RunningCount, described.Services[0].DesiredCount)
	}
}

// TestUpdateStack_ECSServiceTaskDefinitionChange checks that an update swapping
// in a new task definition is complete only once the new deployment is the one
// running. UPDATE_COMPLETE around a service still serving the old revision is
// the failure this guards: the stack says the deploy landed, and nothing that
// reads the stack can tell that it did not.
func TestUpdateStack_ECSServiceTaskDefinitionChange(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	// Given: a deployed stack running one task
	srv := helpers.NewTestServer(t)
	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"ecs-upd-td-stack"},
		"TemplateBody": []string{serviceStackTemplateWithImage("1", "nginx:latest")},
	})
	cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "ecs-upd-td-stack", "CREATE_COMPLETE")

	// When: the stack is updated to a new container image, replacing the task
	// definition and so starting a new deployment
	ur := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    []string{"ecs-upd-td-stack"},
		"TemplateBody": []string{serviceStackTemplateWithImage("1", "nginx:1.27")},
	})
	ur.Body.Close()
	helpers.AssertStatus(t, ur, http.StatusOK)
	waitForStackStatus(t, srv, "ecs-upd-td-stack", "UPDATE_COMPLETE")

	// Then: by the time the stack says UPDATE_COMPLETE the rollout has already
	// happened — one deployment left, and the task running is the new revision.
	resp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "DescribeServices",
		"application/x-amz-json-1.1", map[string]any{
			"cluster": "upd-cluster", "services": []string{"upd-svc"},
		})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var described struct {
		Services []struct {
			TaskDefinition string `json:"taskDefinition"`
			DesiredCount   int    `json:"desiredCount"`
			RunningCount   int    `json:"runningCount"`
			Deployments    []struct {
				Status       string `json:"status"`
				RunningCount int    `json:"runningCount"`
				RolloutState string `json:"rolloutState"`
			} `json:"deployments"`
		} `json:"services"`
	}
	helpers.DecodeJSON(t, resp, &described)
	if len(described.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(described.Services))
	}
	svc := described.Services[0]
	if !strings.HasSuffix(svc.TaskDefinition, "upd-task:2") {
		t.Errorf("expected the service on upd-task:2, got %q", svc.TaskDefinition)
	}
	if len(svc.Deployments) != 1 {
		t.Fatalf("expected the superseded deployment to be gone by UPDATE_COMPLETE, got %#v", svc.Deployments)
	}
	if svc.Deployments[0].RolloutState != "COMPLETED" || svc.Deployments[0].RunningCount != 1 {
		t.Errorf("expected a completed deployment running 1 task, got %#v", svc.Deployments[0])
	}
	if svc.RunningCount != 1 {
		t.Errorf("expected runningCount=1, got %d", svc.RunningCount)
	}

	// And the task actually running is the new revision, not the old one the
	// service merely names.
	listResp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "ListTasks",
		"application/x-amz-json-1.1", map[string]any{"cluster": "upd-cluster", "desiredStatus": "RUNNING"})
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		TaskArns []string `json:"taskArns"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if len(listed.TaskArns) != 1 {
		t.Fatalf("expected 1 running task, got %d", len(listed.TaskArns))
	}
	descResp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "DescribeTasks",
		"application/x-amz-json-1.1", map[string]any{"cluster": "upd-cluster", "tasks": listed.TaskArns})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var tasks struct {
		Tasks []struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"tasks"`
	}
	helpers.DecodeJSON(t, descResp, &tasks)
	if len(tasks.Tasks) != 1 {
		t.Fatalf("expected 1 described task, got %d", len(tasks.Tasks))
	}
	if !strings.HasSuffix(tasks.Tasks[0].TaskDefinitionArn, "upd-task:2") {
		t.Errorf("expected the running task on upd-task:2, got %q", tasks.Tasks[0].TaskDefinitionArn)
	}
}

// TestUpdateStack_ECSServiceFailedUpdateLeavesStackRecoverable checks the shape
// that wedges a real stack: an update whose ECS service resource fails must
// unwind to a terminal state the next deploy can start from, and must not
// leave the stack sitting in UPDATE_IN_PROGRESS forever.
func TestUpdateStack_ECSServiceFailedUpdateLeavesStackRecoverable(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	// Given: a deployed stack.
	srv := helpers.NewTestServer(t)
	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{"ecs-upd-fail-stack"},
		"TemplateBody": []string{serviceStackTemplate("1")},
	})
	cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, "ecs-upd-fail-stack", "CREATE_COMPLETE")

	// When: the update points the service at a task definition that does not
	// exist, so UpdateService fails the way a rolled-back deployment does.
	broken := strings.Replace(serviceStackTemplate("1"),
		`"TaskDefinition": { "Ref": "TaskDefinition" },`,
		`"TaskDefinition": "no-such-family:99",`, 1)
	ur := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    []string{"ecs-upd-fail-stack"},
		"TemplateBody": []string{broken},
	})
	ur.Body.Close()
	helpers.AssertStatus(t, ur, http.StatusOK)

	// Then: the stack reaches a terminal state that says the update failed,
	// rather than reporting UPDATE_COMPLETE around a service that never
	// changed, or hanging in UPDATE_IN_PROGRESS.
	status := waitForStackStatusIn(t, srv, "ecs-upd-fail-stack",
		"UPDATE_ROLLBACK_COMPLETE", "UPDATE_ROLLBACK_FAILED", "UPDATE_FAILED", "UPDATE_COMPLETE")
	if status == "UPDATE_COMPLETE" {
		t.Fatal("a failed service update reported UPDATE_COMPLETE")
	}

	// And the service is still the one that worked, so the next deploy has
	// something to proceed from.
	resp := awsJSONCall(t, srv, "AmazonEC2ContainerServiceV20141113.", "DescribeServices",
		"application/x-amz-json-1.1", map[string]any{
			"cluster": "upd-cluster", "services": []string{"upd-svc"},
		})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var described struct {
		Services []struct {
			Status         string `json:"status"`
			TaskDefinition string `json:"taskDefinition"`
		} `json:"services"`
	}
	helpers.DecodeJSON(t, resp, &described)
	if len(described.Services) != 1 || described.Services[0].Status != "ACTIVE" {
		t.Fatalf("expected the service to survive the failed update, got %#v", described.Services)
	}
	if !strings.Contains(described.Services[0].TaskDefinition, "upd-task") {
		t.Errorf("expected the service to still run the working task definition, got %q",
			described.Services[0].TaskDefinition)
	}
}
