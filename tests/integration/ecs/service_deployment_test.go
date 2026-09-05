package ecs_test

// service_deployment_test.go — what an UpdateService that changes the task
// definition actually does to the tasks.
//
// Swapping the task definition starts a new deployment, and a deployment is a
// rollout: the new revision's tasks are placed, the previous revision's are
// retired, and the service is only at steady state once that has happened.
// Reporting the new deployment COMPLETED while the old revision is still the
// only thing running makes every caller that waits on a rollout — the AWS
// CLI's `services-stable` waiter, CDK, CloudFormation — believe a deploy
// landed when nothing was deployed.
//
// Shares the harness (ecsCall, describeService, pollService) with ecs_test.go
// and service_lifecycle_test.go.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// registerRevision registers another revision of a family, returning its ARN.
func registerRevision(t *testing.T, srv *helpers.TestServer, family, image string) string {
	t.Helper()
	resp := ecsCall(t, srv, "RegisterTaskDefinition", map[string]any{
		"family":                  family,
		"networkMode":             "awsvpc",
		"requiresCompatibilities": []string{"FARGATE"},
		"cpu":                     "256",
		"memory":                  "512",
		"containerDefinitions": []map[string]any{
			{"name": "app", "image": image},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var registered struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	helpers.DecodeJSON(t, resp, &registered)
	return registered.TaskDefinition.TaskDefinitionArn
}

// taskView is the part of a described task that says which revision is really
// running, and which deployment put it there.
type taskView struct {
	TaskArn           string `json:"taskArn"`
	TaskDefinitionArn string `json:"taskDefinitionArn"`
	LastStatus        string `json:"lastStatus"`
	StartedBy         string `json:"startedBy"`
}

// describeClusterTasks describes every task a cluster has ever placed, stopped
// ones included, so a test can tell a replaced task from a missing one.
func describeClusterTasks(t *testing.T, srv *helpers.TestServer, cluster string) []taskView {
	t.Helper()
	listResp := ecsCall(t, srv, "ListTasks", map[string]any{"cluster": cluster})
	defer listResp.Body.Close()
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		TaskArns []string `json:"taskArns"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	if len(listed.TaskArns) == 0 {
		return nil
	}

	descResp := ecsCall(t, srv, "DescribeTasks", map[string]any{
		"cluster": cluster,
		"tasks":   listed.TaskArns,
	})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var described struct {
		Tasks []taskView `json:"tasks"`
	}
	helpers.DecodeJSON(t, descResp, &described)
	return described.Tasks
}

func TestUpdateService_newTaskDefinitionReplacesRunningTasks(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	// Given: a service running one task of revision 1
	srv := awsvpcTaskDefCluster(t, "rollout-upd-cluster", "rollout-upd-task")
	create := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "rollout-upd-cluster",
		"serviceName":    "rollout-upd-service",
		"taskDefinition": "rollout-upd-task:1",
		"desiredCount":   1,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-12345678"}},
		},
	})
	helpers.AssertStatus(t, create, http.StatusOK)
	create.Body.Close()
	awaitServiceEvent(t, srv, "rollout-upd-cluster", "rollout-upd-service", "has reached a steady state")

	// When: the service is updated to a second revision
	revision2 := registerRevision(t, srv, "rollout-upd-task", "nginx:1.27")
	upd := ecsCall(t, srv, "UpdateService", map[string]any{
		"cluster":        "rollout-upd-cluster",
		"service":        "rollout-upd-service",
		"taskDefinition": "rollout-upd-task:2",
	})
	helpers.AssertStatus(t, upd, http.StatusOK)
	upd.Body.Close()

	// Then: the rollout finishes on the new revision — one deployment left,
	// its own tasks running, and nothing still serving revision 1.
	view := pollService(t, srv, "rollout-upd-cluster", "rollout-upd-service", func(v ecsServiceView) bool {
		return len(v.Deployments) == 1 && v.Deployments[0].RolloutState == "COMPLETED"
	})
	if len(view.Deployments) != 1 {
		t.Fatalf("expected the superseded deployment to be retired, got %d deployments (events:\n%s)",
			len(view.Deployments), view.eventMessages())
	}
	d := view.Deployments[0]
	if d.RolloutState != "COMPLETED" {
		t.Errorf("expected rolloutState=COMPLETED, got %q (reason %q)", d.RolloutState, d.RolloutStateReason)
	}
	if d.RunningCount != 1 {
		t.Errorf("expected the new deployment to be running 1 task, got %d", d.RunningCount)
	}
	if view.RunningCount != 1 {
		t.Errorf("expected runningCount=1 after the rollout, got %d (events:\n%s)",
			view.RunningCount, view.eventMessages())
	}

	tasks := describeClusterTasks(t, srv, "rollout-upd-cluster")
	running := make([]taskView, 0, len(tasks))
	for _, task := range tasks {
		if task.LastStatus != "STOPPED" {
			running = append(running, task)
		}
	}
	if len(running) != 1 {
		t.Fatalf("expected exactly 1 live task after the rollout, got %d (%#v)", len(running), running)
	}
	if running[0].TaskDefinitionArn != revision2 {
		t.Errorf("expected the live task to run %s, got %s", revision2, running[0].TaskDefinitionArn)
	}
	if running[0].StartedBy != d.ID {
		t.Errorf("expected the live task to belong to the new deployment %s, got %s", d.ID, running[0].StartedBy)
	}
	if !strings.Contains(view.eventMessages(), "was updated to use task definition") {
		t.Errorf("expected the task-definition update event, got:\n%s", view.eventMessages())
	}
}

func TestUpdateService_newDeploymentDoesNotCountTheOldDeploymentsTasks(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	// Given: a service at steady state on revision 1
	srv := awsvpcTaskDefCluster(t, "count-upd-cluster", "count-upd-task")
	create := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "count-upd-cluster",
		"serviceName":    "count-upd-service",
		"taskDefinition": "count-upd-task:1",
		"desiredCount":   1,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-12345678"}},
		},
	})
	helpers.AssertStatus(t, create, http.StatusOK)
	create.Body.Close()
	awaitServiceEvent(t, srv, "count-upd-cluster", "count-upd-service", "has reached a steady state")

	// When: a new revision is rolled out
	registerRevision(t, srv, "count-upd-task", "nginx:1.27")
	upd := ecsCall(t, srv, "UpdateService", map[string]any{
		"cluster":        "count-upd-cluster",
		"service":        "count-upd-service",
		"taskDefinition": "count-upd-task:2",
	})
	defer upd.Body.Close()
	helpers.AssertStatus(t, upd, http.StatusOK)

	// Then: the response — the state the moment the new deployment was
	// created, before any of its tasks could start — reports the new
	// deployment as still in progress with none of its own tasks running.
	// Crediting it with the previous deployment's task is what lets a waiter
	// call a rollout complete before it has begun.
	var updated struct {
		Service ecsServiceView `json:"service"`
	}
	helpers.DecodeJSON(t, upd, &updated)
	if len(updated.Service.Deployments) != 2 {
		t.Fatalf("expected the new and superseded deployments, got %#v", updated.Service.Deployments)
	}
	primary := updated.Service.Deployments[0]
	if primary.Status != "PRIMARY" {
		t.Fatalf("expected the new deployment first, got %#v", updated.Service.Deployments)
	}
	if primary.RunningCount != 0 {
		t.Errorf("expected the new deployment to have 0 running tasks of its own, got %d", primary.RunningCount)
	}
	if primary.RolloutState != "IN_PROGRESS" {
		t.Errorf("expected rolloutState=IN_PROGRESS while the new deployment is placing tasks, got %q (reason %q)",
			primary.RolloutState, primary.RolloutStateReason)
	}
}

func TestUpdateService_forceNewDeploymentStartsFreshTasks(t *testing.T) {
	// Given: a service with one task placed from its current task definition.
	srv := helpers.NewTestServer(t)
	cluster := ecsCall(t, srv, "CreateCluster", map[string]any{"clusterName": "forced-cluster"})
	helpers.AssertStatus(t, cluster, http.StatusOK)
	cluster.Body.Close()
	register := ecsCall(t, srv, "RegisterTaskDefinition", map[string]any{
		"family":               "forced-task",
		"containerDefinitions": []map[string]any{{"name": "app", "image": "busybox"}},
	})
	helpers.AssertStatus(t, register, http.StatusOK)
	register.Body.Close()
	create := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster": "forced-cluster", "serviceName": "forced-service",
		"taskDefinition": "forced-task:1", "desiredCount": 1,
	})
	helpers.AssertStatus(t, create, http.StatusOK)
	var created struct {
		Service ecsServiceView `json:"service"`
	}
	helpers.DecodeJSON(t, create, &created)
	create.Body.Close()
	if len(created.Service.Deployments) != 1 {
		t.Fatalf("initial deployments = %#v, want one", created.Service.Deployments)
	}
	initialDeploymentID := created.Service.Deployments[0].ID

	// When: the AWS CLI-shaped request forces a deployment without changing
	// the task definition.
	update := ecsCall(t, srv, "UpdateService", map[string]any{
		"cluster": "forced-cluster", "service": "forced-service", "forceNewDeployment": true,
	})
	defer update.Body.Close()
	helpers.AssertStatus(t, update, http.StatusOK)
	var updated struct {
		Service ecsServiceView `json:"service"`
	}
	helpers.DecodeJSON(t, update, &updated)

	// Then: a distinct PRIMARY deployment owns a freshly placed task while the
	// prior deployment is retained until its replacement becomes healthy.
	if len(updated.Service.Deployments) != 2 {
		t.Fatalf("deployments = %#v, want new and superseded deployments", updated.Service.Deployments)
	}
	primary := updated.Service.Deployments[0]
	if primary.ID == initialDeploymentID {
		t.Errorf("forced deployment reused ID %q", primary.ID)
	}
	if primary.Status != "PRIMARY" || primary.PendingCount != 1 {
		t.Errorf("forced PRIMARY deployment = %#v, want one freshly placed pending task", primary)
	}
	if updated.Service.Deployments[1].ID != initialDeploymentID || updated.Service.Deployments[1].Status != "ACTIVE" {
		t.Errorf("superseded deployment = %#v, want prior deployment %q ACTIVE",
			updated.Service.Deployments[1], initialDeploymentID)
	}
}
