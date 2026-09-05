package ecs_test

// service_lifecycle_test.go — the path a service actually takes: what AWS
// requires before it will create one, the tasks it then places, and how it
// reports the rollout. Shares ecsCall and the rest of the harness with
// ecs_test.go.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── awsvpc networkConfiguration is gated on the task definition ─────────────
//
// AWS requires networkConfiguration when the *task definition's* networkMode is
// awsvpc. Gating it on launchType instead lets through the two shapes that
// produce a service which can never place a task: an awsvpc task definition run
// under EC2, and the Fargate shape CDK emits by default, where the launch type
// travels in a capacityProviderStrategy and launchType is absent.

// awsvpcTaskDefCluster registers an awsvpc/Fargate task definition in a fresh
// cluster and returns the server.
func awsvpcTaskDefCluster(t *testing.T, cluster, family string) *helpers.TestServer {
	t.Helper()
	srv := helpers.NewTestServer(t)

	cr := ecsCall(t, srv, "CreateCluster", map[string]any{"clusterName": cluster})
	helpers.AssertStatus(t, cr, http.StatusOK)
	cr.Body.Close()

	reg := ecsCall(t, srv, "RegisterTaskDefinition", map[string]any{
		"family":                  family,
		"networkMode":             "awsvpc",
		"requiresCompatibilities": []string{"FARGATE"},
		"cpu":                     "256",
		"memory":                  "512",
		"containerDefinitions": []map[string]any{
			{"name": "app", "image": "nginx:latest"},
		},
	})
	helpers.AssertStatus(t, reg, http.StatusOK)
	reg.Body.Close()
	return srv
}

func assertInvalidParameter(t *testing.T, resp *http.Response, wantMessage string) {
	t.Helper()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var result struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !strings.Contains(result.Type, "InvalidParameterException") {
		t.Errorf("expected InvalidParameterException, got %q", result.Type)
	}
	if !strings.Contains(result.Message, wantMessage) {
		t.Errorf("expected message containing %q, got %q", wantMessage, result.Message)
	}
}

func TestCreateService_awsvpcTaskDefWithoutLaunchType_requiresNetworkConfiguration(t *testing.T) {
	// Given: an awsvpc task definition
	srv := awsvpcTaskDefCluster(t, "capacity-cluster", "capacity-task")

	// When: a service is created the way CDK emits one — the launch type
	// carried by a capacityProviderStrategy rather than launchType — and no
	// networkConfiguration is supplied
	resp := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "capacity-cluster",
		"serviceName":    "capacity-service",
		"taskDefinition": "capacity-task:1",
		"desiredCount":   1,
		"capacityProviderStrategy": []map[string]any{
			{"capacityProvider": "FARGATE", "weight": 1},
		},
	})
	defer resp.Body.Close()

	// Then: it is rejected, rather than creating a service that starts nothing
	assertInvalidParameter(t, resp, "Network Configuration must be provided when networkMode is 'awsvpc'.")
}

func TestCreateService_awsvpcTaskDefUnderEC2LaunchType_requiresNetworkConfiguration(t *testing.T) {
	// Given: an awsvpc task definition
	srv := awsvpcTaskDefCluster(t, "ec2-cluster", "ec2-task")

	// When: a service is created for it under the EC2 launch type with no
	// networkConfiguration
	resp := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "ec2-cluster",
		"serviceName":    "ec2-service",
		"taskDefinition": "ec2-task:1",
		"desiredCount":   1,
		"launchType":     "EC2",
	})
	defer resp.Body.Close()

	// Then: it is rejected — the requirement follows networkMode, not launchType
	assertInvalidParameter(t, resp, "Network Configuration must be provided when networkMode is 'awsvpc'.")
}

func TestCreateService_awsvpcWithEmptySubnets_rejected(t *testing.T) {
	// Given: an awsvpc task definition
	srv := awsvpcTaskDefCluster(t, "empty-subnet-cluster", "empty-subnet-task")

	// When: a service supplies an awsvpcConfiguration carrying no subnet
	resp := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "empty-subnet-cluster",
		"serviceName":    "empty-subnet-service",
		"taskDefinition": "empty-subnet-task:1",
		"desiredCount":   1,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{}},
		},
	})
	defer resp.Body.Close()

	// Then: it is rejected — an awsvpc task with no subnet is unplaceable
	assertInvalidParameter(t, resp, "subnets can not be empty.")
}

func TestRunTask_awsvpcTaskDefWithoutLaunchType_requiresNetworkConfiguration(t *testing.T) {
	// Given: an awsvpc task definition
	srv := awsvpcTaskDefCluster(t, "runtask-cluster", "runtask-task")

	// When: RunTask omits both launchType and networkConfiguration
	resp := ecsCall(t, srv, "RunTask", map[string]any{
		"cluster":        "runtask-cluster",
		"taskDefinition": "runtask-task:1",
	})
	defer resp.Body.Close()

	// Then: it is rejected
	assertInvalidParameter(t, resp, "Network Configuration must be provided when networkMode is 'awsvpc'.")
}

// ─── Services place tasks and report a rollout ───────────────────────────────

type ecsServiceView struct {
	ServiceName  string `json:"serviceName"`
	DesiredCount int    `json:"desiredCount"`
	RunningCount int    `json:"runningCount"`
	PendingCount int    `json:"pendingCount"`
	Status       string `json:"status"`
	Deployments  []struct {
		ID                 string `json:"id"`
		Status             string `json:"status"`
		DesiredCount       int    `json:"desiredCount"`
		RunningCount       int    `json:"runningCount"`
		PendingCount       int    `json:"pendingCount"`
		FailedTasks        int    `json:"failedTasks"`
		RolloutState       string `json:"rolloutState"`
		RolloutStateReason string `json:"rolloutStateReason"`
	} `json:"deployments"`
	Events []struct {
		Message string `json:"message"`
	} `json:"events"`
}

func (v ecsServiceView) eventMessages() string {
	var b strings.Builder
	for _, e := range v.Events {
		b.WriteString(e.Message)
		b.WriteString("\n")
	}
	return b.String()
}

// describeService reads one service back, retrying until its tasks have settled
// so assertions see the state after the scheduler's start transition.
func describeService(t *testing.T, srv *helpers.TestServer, cluster, service string) ecsServiceView {
	t.Helper()
	return pollService(t, srv, cluster, service, func(v ecsServiceView) bool {
		return v.RunningCount >= v.DesiredCount
	})
}

// awaitServiceEvent polls until the service has recorded an event containing
// want. Counts are recomputed on the read path, so a service can report its
// desired count before the scheduler has persisted the event announcing it —
// asserting on the event means waiting for the event.
func awaitServiceEvent(t *testing.T, srv *helpers.TestServer, cluster, service, want string) ecsServiceView {
	t.Helper()
	return pollService(t, srv, cluster, service, func(v ecsServiceView) bool {
		return strings.Contains(v.eventMessages(), want)
	})
}

// pollService reads a service until done reports true, or the deadline passes —
// returning the last view either way so the caller's assertions produce the
// useful failure message rather than this helper.
//
// The budget is generous because a deployment does not reach its steady state
// until its tasks have stayed up for a settle window — see settleWindow in
// package ecs — so anything waiting on COMPLETED, or on the steady-state event,
// waits seconds rather than milliseconds. It costs nothing when the condition
// holds early: every caller here is waiting for something that does arrive.
func pollService(t *testing.T, srv *helpers.TestServer, cluster, service string, done func(ecsServiceView) bool) ecsServiceView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp := ecsCall(t, srv, "DescribeServices", map[string]any{
			"cluster":  cluster,
			"services": []string{service},
		})
		helpers.AssertStatus(t, resp, http.StatusOK)
		var result struct {
			Services []ecsServiceView `json:"services"`
		}
		helpers.DecodeJSON(t, resp, &result)
		resp.Body.Close()
		if len(result.Services) == 0 {
			t.Fatalf("service %s not found", service)
		}
		view := result.Services[0]
		if done(view) || time.Now().After(deadline) {
			return view
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCreateService_placesTasksAndReachesSteadyState(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	// Given: an awsvpc task definition in a cluster
	srv := awsvpcTaskDefCluster(t, "rollout-cluster", "rollout-task")

	// When: a service is created wanting two tasks
	resp := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "rollout-cluster",
		"serviceName":    "rollout-service",
		"taskDefinition": "rollout-task:1",
		"desiredCount":   2,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{
				"subnets":        []string{"subnet-12345678"},
				"assignPublicIp": "ENABLED",
			},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: the service reaches its desired count and reports a completed rollout
	view := awaitServiceEvent(t, srv, "rollout-cluster", "rollout-service", "has reached a steady state")
	if view.RunningCount != 2 {
		t.Errorf("expected runningCount=2, got %d (events:\n%s)", view.RunningCount, view.eventMessages())
	}
	if len(view.Deployments) == 0 {
		t.Fatal("expected a PRIMARY deployment")
	}
	d := view.Deployments[0]
	if d.RolloutState != "COMPLETED" {
		t.Errorf("expected rolloutState=COMPLETED, got %q (reason %q)", d.RolloutState, d.RolloutStateReason)
	}
	if d.RunningCount != 2 {
		t.Errorf("expected deployment runningCount=2, got %d", d.RunningCount)
	}
	if d.FailedTasks != 0 {
		t.Errorf("expected failedTasks=0, got %d", d.FailedTasks)
	}

	events := view.eventMessages()
	if !strings.Contains(events, "has started 2 tasks") {
		t.Errorf("expected a task-start event, got:\n%s", events)
	}
	if !strings.Contains(events, "has reached a steady state") {
		t.Errorf("expected a steady-state event, got:\n%s", events)
	}
}

func TestCreateService_tasksCarryServiceNetworkingAndDeploymentID(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	// Given: an awsvpc task definition in a cluster
	srv := awsvpcTaskDefCluster(t, "task-shape-cluster", "task-shape-task")

	// When: a service is created with a networkConfiguration
	resp := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "task-shape-cluster",
		"serviceName":    "task-shape-service",
		"taskDefinition": "task-shape-task:1",
		"desiredCount":   1,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{
				"subnets":        []string{"subnet-abcdef12"},
				"assignPublicIp": "DISABLED",
			},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	view := describeService(t, srv, "task-shape-cluster", "task-shape-service")
	if len(view.Deployments) == 0 {
		t.Fatal("expected a PRIMARY deployment")
	}
	deploymentID := view.Deployments[0].ID

	// Then: the task the service placed is shaped like one RunTask would place —
	// it carries the service's networking, its ENI attachment, the Fargate
	// launch type and platform version, and the deployment that started it.
	listResp := ecsCall(t, srv, "ListTasks", map[string]any{"cluster": "task-shape-cluster"})
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		TaskArns []string `json:"taskArns"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	listResp.Body.Close()
	if len(listed.TaskArns) != 1 {
		t.Fatalf("expected 1 task, got %d", len(listed.TaskArns))
	}

	descResp := ecsCall(t, srv, "DescribeTasks", map[string]any{
		"cluster": "task-shape-cluster",
		"tasks":   listed.TaskArns,
	})
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var described struct {
		Tasks []struct {
			LaunchType      string `json:"launchType"`
			PlatformVersion string `json:"platformVersion"`
			StartedBy       string `json:"startedBy"`
			Group           string `json:"group"`
			Attachments     []struct {
				Type    string `json:"type"`
				Details []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"details"`
			} `json:"attachments"`
		} `json:"tasks"`
	}
	helpers.DecodeJSON(t, descResp, &described)
	descResp.Body.Close()
	if len(described.Tasks) != 1 {
		t.Fatalf("expected 1 described task, got %d", len(described.Tasks))
	}
	task := described.Tasks[0]

	if task.LaunchType != "FARGATE" {
		t.Errorf("expected launchType=FARGATE, got %q", task.LaunchType)
	}
	if task.PlatformVersion != "LATEST" {
		t.Errorf("expected platformVersion=LATEST, got %q", task.PlatformVersion)
	}
	if task.Group != "service:task-shape-service" {
		t.Errorf("expected group=service:task-shape-service, got %q", task.Group)
	}
	if task.StartedBy != deploymentID {
		t.Errorf("expected startedBy=%q (the deployment ID), got %q", deploymentID, task.StartedBy)
	}
	if len(task.Attachments) != 1 || task.Attachments[0].Type != "ElasticNetworkInterface" {
		t.Fatalf("expected an ElasticNetworkInterface attachment, got %#v", task.Attachments)
	}
	// The service's networking reaches the task through its attachment, not a
	// networkConfiguration member — AWS's Task shape has no such member; only
	// Service, Deployment and TaskSet carry one.
	var sawENI, sawSubnet bool
	for _, d := range task.Attachments[0].Details {
		if d.Name == "networkInterfaceId" && strings.HasPrefix(d.Value, "eni-") {
			sawENI = true
		}
		if d.Name == "subnetId" && d.Value == "subnet-abcdef12" {
			sawSubnet = true
		}
	}
	if !sawENI {
		t.Errorf("expected an eni- network interface ID, got %#v", task.Attachments[0].Details)
	}
	if !sawSubnet {
		t.Errorf("expected the service's subnet on the task's attachment, got %#v", task.Attachments[0].Details)
	}
}

func TestUpdateService_scaleDownStopsTasksWithSchedulerStopCode(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	// Given: a service running two tasks
	srv := awsvpcTaskDefCluster(t, "scale-cluster", "scale-task")
	resp := ecsCall(t, srv, "CreateService", map[string]any{
		"cluster":        "scale-cluster",
		"serviceName":    "scale-service",
		"taskDefinition": "scale-task:1",
		"desiredCount":   2,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-12345678"}},
		},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()
	describeService(t, srv, "scale-cluster", "scale-service")

	// When: the service is scaled to one task
	upd := ecsCall(t, srv, "UpdateService", map[string]any{
		"cluster":      "scale-cluster",
		"service":      "scale-service",
		"desiredCount": 1,
	})
	helpers.AssertStatus(t, upd, http.StatusOK)
	upd.Body.Close()

	// Then: the stopped task carries the stop code AWS uses for a scheduler
	// decision, rather than no code at all
	listResp := ecsCall(t, srv, "ListTasks", map[string]any{
		"cluster":       "scale-cluster",
		"desiredStatus": "STOPPED",
	})
	helpers.AssertStatus(t, listResp, http.StatusOK)
	var listed struct {
		TaskArns []string `json:"taskArns"`
	}
	helpers.DecodeJSON(t, listResp, &listed)
	listResp.Body.Close()
	if len(listed.TaskArns) != 1 {
		t.Fatalf("expected 1 stopped task, got %d", len(listed.TaskArns))
	}

	descResp := ecsCall(t, srv, "DescribeTasks", map[string]any{
		"cluster": "scale-cluster",
		"tasks":   listed.TaskArns,
	})
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var described struct {
		Tasks []struct {
			StopCode      string `json:"stopCode"`
			StoppedReason string `json:"stoppedReason"`
			StoppingAt    *int64 `json:"stoppingAt"`
			StoppedAt     *int64 `json:"stoppedAt"`
		} `json:"tasks"`
	}
	helpers.DecodeJSON(t, descResp, &described)
	descResp.Body.Close()
	if len(described.Tasks) != 1 {
		t.Fatalf("expected 1 described task, got %d", len(described.Tasks))
	}
	if described.Tasks[0].StopCode != "ServiceSchedulerInitiated" {
		t.Errorf("expected stopCode=ServiceSchedulerInitiated, got %q", described.Tasks[0].StopCode)
	}
	if described.Tasks[0].StoppingAt == nil || described.Tasks[0].StoppedAt == nil {
		t.Errorf("expected scheduler stop timestamps, got stoppingAt=%v stoppedAt=%v", described.Tasks[0].StoppingAt, described.Tasks[0].StoppedAt)
	}
}

// ─── The typed (RPC v2 CBOR) path enforces the same rules ───────────────────
//
// CreateService and RunTask are implemented twice — once for the JSON 1.1
// protocol the current SDKs speak, once for RPC v2 CBOR. Newer SDK releases
// move services onto CBOR, so a rule enforced on only one path is a rule that
// silently lapses when a caller upgrades.

func TestRPCv2CBOR_CreateService_requiresNetworkConfigurationForAwsvpc(t *testing.T) {
	// Given: an awsvpc task definition
	srv := awsvpcTaskDefCluster(t, "cbor-svc-cluster", "cbor-svc-task")

	// When: a service is created over CBOR with no networkConfiguration
	resp := ecsCBORCall(t, srv, "CreateService", map[string]any{
		"cluster":        "cbor-svc-cluster",
		"serviceName":    "cbor-service",
		"taskDefinition": "cbor-svc-task:1",
		"desiredCount":   1,
		"launchType":     "FARGATE",
	})
	defer resp.Body.Close()

	// Then: it is rejected, exactly as over JSON
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestRPCv2CBOR_CreateService_placesTasks(t *testing.T) {
	helpers.SkipUnvalidatedDockerTest(t)
	// Given: an awsvpc task definition
	srv := awsvpcTaskDefCluster(t, "cbor-run-cluster", "cbor-run-task")

	// When: a well-formed service is created over CBOR
	resp := ecsCBORCall(t, srv, "CreateService", map[string]any{
		"cluster":        "cbor-run-cluster",
		"serviceName":    "cbor-run-service",
		"taskDefinition": "cbor-run-task:1",
		"desiredCount":   1,
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-12345678"}},
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		Service struct {
			DesiredCount int `cbor:"desiredCount"`
			Deployments  []struct {
				RolloutState string `cbor:"rolloutState"`
			} `cbor:"deployments"`
		} `cbor:"service"`
	}
	decodeCBOR(t, resp, &out)
	if out.Service.DesiredCount != 1 {
		t.Errorf("expected desiredCount=1, got %d", out.Service.DesiredCount)
	}
	if len(out.Service.Deployments) == 0 || out.Service.Deployments[0].RolloutState == "" {
		t.Errorf("expected a deployment carrying a rollout state, got %#v", out.Service.Deployments)
	}

	// Then: the service settles at its desired count
	view := describeService(t, srv, "cbor-run-cluster", "cbor-run-service")
	if view.RunningCount != 1 {
		t.Errorf("expected runningCount=1, got %d (events:\n%s)", view.RunningCount, view.eventMessages())
	}
}

func TestStopTask_setsUserInitiatedStopCode(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	// Given: a running task
	srv := awsvpcTaskDefCluster(t, "stopcode-cluster", "stopcode-task")
	runResp := ecsCall(t, srv, "RunTask", map[string]any{
		"cluster":        "stopcode-cluster",
		"taskDefinition": "stopcode-task:1",
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-12345678"}},
		},
	})
	helpers.AssertStatus(t, runResp, http.StatusOK)
	var run struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	helpers.DecodeJSON(t, runResp, &run)
	runResp.Body.Close()
	if len(run.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(run.Tasks))
	}
	tagResp := ecsCall(t, srv, "TagResource", map[string]any{
		"resourceArn": run.Tasks[0].TaskArn,
		"tags":        []map[string]string{{"key": "lifecycle", "value": "temporary"}},
	})
	helpers.AssertStatus(t, tagResp, http.StatusOK)
	tagResp.Body.Close()

	// When: the caller stops it
	stopResp := ecsCall(t, srv, "StopTask", map[string]any{
		"cluster": "stopcode-cluster",
		"task":    run.Tasks[0].TaskArn,
		"reason":  "done with it",
	})
	helpers.AssertStatus(t, stopResp, http.StatusOK)
	var stopped struct {
		Task struct {
			StopCode      string `json:"stopCode"`
			StoppedReason string `json:"stoppedReason"`
		} `json:"task"`
	}
	helpers.DecodeJSON(t, stopResp, &stopped)
	stopResp.Body.Close()

	// Then: the task carries the stop code AWS uses for a caller-initiated stop
	if stopped.Task.StopCode != "UserInitiated" {
		t.Errorf("expected stopCode=UserInitiated, got %q", stopped.Task.StopCode)
	}
	if stopped.Task.StoppedReason != "done with it" {
		t.Errorf("expected the caller's reason, got %q", stopped.Task.StoppedReason)
	}

	listTags := ecsCall(t, srv, "ListTagsForResource", map[string]any{
		"resourceArn": run.Tasks[0].TaskArn,
	})
	defer listTags.Body.Close()
	helpers.AssertStatus(t, listTags, http.StatusOK)
	var tags struct {
		Tags []map[string]string `json:"tags"`
	}
	helpers.DecodeJSON(t, listTags, &tags)
	if len(tags.Tags) != 0 {
		t.Fatalf("expected StopTask to delete task tags, got %v", tags.Tags)
	}
}

func TestStopTask_withoutReason(t *testing.T) {
	helpers.SkipWithoutDocker(t)
	// Given: a running standalone task.
	srv := awsvpcTaskDefCluster(t, "default-reason-cluster", "default-reason-task")
	run := ecsCall(t, srv, "RunTask", map[string]any{
		"cluster":        "default-reason-cluster",
		"taskDefinition": "default-reason-task:1",
		"launchType":     "FARGATE",
		"networkConfiguration": map[string]any{
			"awsvpcConfiguration": map[string]any{"subnets": []string{"subnet-12345678"}},
		},
	})
	helpers.AssertStatus(t, run, http.StatusOK)
	var started struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	helpers.DecodeJSON(t, run, &started)
	run.Body.Close()

	// When: StopTask omits the optional reason.
	stopped := ecsCall(t, srv, "StopTask", map[string]any{
		"cluster": "default-reason-cluster", "task": started.Tasks[0].TaskArn,
	})
	defer stopped.Body.Close()
	helpers.AssertStatus(t, stopped, http.StatusOK)
	var result struct {
		Task struct {
			StoppedReason string `json:"stoppedReason"`
		} `json:"task"`
	}
	helpers.DecodeJSON(t, stopped, &result)

	// Then: AWS's documented default reason is returned.
	if result.Task.StoppedReason != "Task stopped by user" {
		t.Fatalf("expected AWS default stoppedReason, got %q", result.Task.StoppedReason)
	}
}
