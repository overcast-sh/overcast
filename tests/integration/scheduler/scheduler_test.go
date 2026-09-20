// Package scheduler_test contains integration tests for the EventBridge Scheduler emulator.
//
// Run: go test ./tests/integration/scheduler/...
package scheduler_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// schDo performs a Scheduler REST-JSON request the way an AWS SDK does: at
// AWS's own path, carrying a SigV4 credential scope that names the scheduler
// service. That scope is what classifies the request for logging and IAM, so
// signing here keeps the suite on the code path a real client takes.
func schDo(t *testing.T, srv *helpers.TestServer, method, path string, body any) *http.Response {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reqBody = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20260809/us-east-1/scheduler/aws4_request, "+
			"SignedHeaders=host;x-amz-date, Signature=0000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// createGroup creates a schedule group and returns its ARN.
func createGroup(t *testing.T, srv *helpers.TestServer, name string) string {
	t.Helper()
	resp := schDo(t, srv, http.MethodPost, "/schedule-groups/"+name, map[string]any{
		"Tags": map[string]any{},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ScheduleGroupArn string `json:"ScheduleGroupArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ScheduleGroupArn == "" {
		t.Fatal("expected ScheduleGroupArn in response")
	}
	return result.ScheduleGroupArn
}

// createSchedule creates a schedule in the given group and returns its ARN.
func createSchedule(t *testing.T, srv *helpers.TestServer, group, name, expression string) string {
	t.Helper()
	resp := schDo(t, srv, http.MethodPost, "/schedules/"+name, map[string]any{
		"GroupName":          group,
		"ScheduleExpression": expression,
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ScheduleArn string `json:"ScheduleArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.ScheduleArn == "" {
		t.Fatal("expected ScheduleArn in response")
	}
	return result.ScheduleArn
}

// ─── Schedule Group Tests ─────────────────────────────────────────────────────

func TestCreateScheduleGroup_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateScheduleGroup is called
	resp := schDo(t, srv, http.MethodPost, "/schedule-groups/my-group", map[string]any{
		"Tags": map[string]any{"Env": "test"},
	})
	defer resp.Body.Close()

	// Then: 200 with ScheduleGroupArn
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ScheduleGroupArn string `json:"ScheduleGroupArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !strings.Contains(result.ScheduleGroupArn, "my-group") {
		t.Errorf("expected ARN to contain group name, got %q", result.ScheduleGroupArn)
	}
}

func TestCreateScheduleGroup_duplicate(t *testing.T) {
	// Given: a group already exists
	srv := helpers.NewTestServer(t)
	schDo(t, srv, http.MethodPost, "/schedule-groups/dup-group", map[string]any{}).Body.Close()

	// When: CreateScheduleGroup is called again with same name
	resp := schDo(t, srv, http.MethodPost, "/schedule-groups/dup-group", map[string]any{})
	defer resp.Body.Close()

	// Then: 409 Conflict
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestGetScheduleGroup_success(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "get-group")

	// When: GetScheduleGroup is called
	resp := schDo(t, srv, http.MethodGet, "/schedule-groups/get-group", nil)
	defer resp.Body.Close()

	// Then: 200 with group details
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name  string `json:"Name"`
		State string `json:"State"`
		Arn   string `json:"Arn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Name != "get-group" {
		t.Errorf("expected Name=get-group, got %q", result.Name)
	}
	if result.State != "ACTIVE" {
		t.Errorf("expected State=ACTIVE, got %q", result.State)
	}
}

func TestGetScheduleGroup_notFound(t *testing.T) {
	// Given: no groups exist
	srv := helpers.NewTestServer(t)

	// When: GetScheduleGroup is called for unknown group
	resp := schDo(t, srv, http.MethodGet, "/schedule-groups/no-such-group", nil)
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListScheduleGroups_success(t *testing.T) {
	// Given: two groups exist
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "group-a")
	createGroup(t, srv, "group-b")

	// When: ListScheduleGroups is called
	resp := schDo(t, srv, http.MethodGet, "/schedule-groups", nil)
	defer resp.Body.Close()

	// Then: 200 with both groups + "default"
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ScheduleGroups []struct {
			Name string `json:"Name"`
		} `json:"ScheduleGroups"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.ScheduleGroups) < 3 { // default + group-a + group-b
		t.Errorf("expected at least 3 groups, got %d", len(result.ScheduleGroups))
	}
}

func TestDeleteScheduleGroup_success(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "del-group")

	// When: DeleteScheduleGroup is called
	resp := schDo(t, srv, http.MethodDelete, "/schedule-groups/del-group", nil)
	resp.Body.Close()

	// Then: 200, and subsequent Get returns 404
	helpers.AssertStatus(t, resp, http.StatusOK)
	get := schDo(t, srv, http.MethodGet, "/schedule-groups/del-group", nil)
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusNotFound)
}

// ─── Schedule Tests ───────────────────────────────────────────────────────────

func TestCreateSchedule_success(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "sched-group")

	// When: CreateSchedule is called
	resp := schDo(t, srv, http.MethodPost, "/schedules/my-schedule", map[string]any{
		"GroupName":          "sched-group",
		"ScheduleExpression": "rate(5 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 200 with ScheduleArn
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		ScheduleArn string `json:"ScheduleArn"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if !strings.Contains(result.ScheduleArn, "my-schedule") {
		t.Errorf("expected ARN to contain schedule name, got %q", result.ScheduleArn)
	}
}

func TestCreateSchedule_defaultGroup(t *testing.T) {
	// Given: no explicit group (uses implicit "default" group)
	srv := helpers.NewTestServer(t)

	// When: CreateSchedule is called with no GroupName in the body
	resp := schDo(t, srv, http.MethodPost, "/schedules/my-default-schedule", map[string]any{
		"ScheduleExpression": "rate(1 hour)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:sqs:us-east-1:000000000000:my-queue",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 200
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestCreateSchedule_duplicateName(t *testing.T) {
	// Given: a schedule already exists in a group
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "dup-sched-group")
	createSchedule(t, srv, "dup-sched-group", "dup-schedule", "rate(5 minutes)")

	// When: CreateSchedule is called again with the same name in the same group
	resp := schDo(t, srv, http.MethodPost, "/schedules/dup-schedule", map[string]any{
		"GroupName":          "dup-sched-group",
		"ScheduleExpression": "rate(10 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 409 Conflict, mirroring CreateScheduleGroup's duplicate handling
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertJSONError(t, resp, "ConflictException")
}

func TestCreateSchedule_duplicateNameDifferentGroupAllowed(t *testing.T) {
	// Given: a schedule exists in one group
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "group-one")
	createGroup(t, srv, "group-two")
	createSchedule(t, srv, "group-one", "shared-name", "rate(5 minutes)")

	// When: a schedule with the same name is created in a different group
	resp := schDo(t, srv, http.MethodPost, "/schedules/shared-name", map[string]any{
		"GroupName":          "group-two",
		"ScheduleExpression": "rate(10 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 200 — group scopes the name, so this is not a duplicate
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestCreateSchedule_flexibleWindowMissingMaximum(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "flex-group")

	// When: CreateSchedule is called with Mode=FLEXIBLE and no MaximumWindowInMinutes
	resp := schDo(t, srv, http.MethodPost, "/schedules/flex-missing-max", map[string]any{
		"GroupName":          "flex-group",
		"ScheduleExpression": "rate(5 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "FLEXIBLE"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 400 ValidationException — AWS's user guide documents that
	// MaximumWindowInMinutes must be set once Mode is FLEXIBLE.
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestCreateSchedule_flexibleWindowOutOfRangeTooLow(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "flex-range-group")

	// When: CreateSchedule is called with MaximumWindowInMinutes below the
	// documented range (Minimum value of 1)
	resp := schDo(t, srv, http.MethodPost, "/schedules/flex-too-low", map[string]any{
		"GroupName":          "flex-range-group",
		"ScheduleExpression": "rate(5 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "FLEXIBLE", "MaximumWindowInMinutes": 0},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 400 ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestCreateSchedule_flexibleWindowOutOfRangeTooHigh(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "flex-range-group-high")

	// When: CreateSchedule is called with MaximumWindowInMinutes above the
	// documented range (Maximum value of 1440)
	resp := schDo(t, srv, http.MethodPost, "/schedules/flex-too-high", map[string]any{
		"GroupName":          "flex-range-group-high",
		"ScheduleExpression": "rate(5 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "FLEXIBLE", "MaximumWindowInMinutes": 1441},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 400 ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestCreateSchedule_offWindowWithMaximumSet(t *testing.T) {
	// Given: a schedule group exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "off-with-max-group")

	// When: CreateSchedule is called with Mode=OFF but MaximumWindowInMinutes set
	resp := schDo(t, srv, http.MethodPost, "/schedules/off-with-max", map[string]any{
		"GroupName":          "off-with-max-group",
		"ScheduleExpression": "rate(5 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF", "MaximumWindowInMinutes": 15},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 400 ValidationException — an OFF window carries no window size
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestGetSchedule_success(t *testing.T) {
	// Given: a schedule exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "g1")
	createSchedule(t, srv, "g1", "sched1", "rate(10 minutes)")

	// When: GetSchedule is called
	resp := schDo(t, srv, http.MethodGet, "/schedules/sched1?groupName=g1", nil)
	defer resp.Body.Close()

	// Then: 200 with schedule details
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Name               string `json:"Name"`
		GroupName          string `json:"GroupName"`
		ScheduleExpression string `json:"ScheduleExpression"`
		State              string `json:"State"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if result.Name != "sched1" {
		t.Errorf("expected Name=sched1, got %q", result.Name)
	}
	if result.GroupName != "g1" {
		t.Errorf("expected GroupName=g1, got %q", result.GroupName)
	}
	if result.ScheduleExpression != "rate(10 minutes)" {
		t.Errorf("expected expression rate(10 minutes), got %q", result.ScheduleExpression)
	}
	if result.State != "ENABLED" {
		t.Errorf("expected State=ENABLED, got %q", result.State)
	}
}

func TestGetSchedule_notFound(t *testing.T) {
	// Given: no schedules exist
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "g2")

	// When: GetSchedule is called for non-existent schedule
	resp := schDo(t, srv, http.MethodGet, "/schedules/no-such-schedule?groupName=g2", nil)
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateSchedule_success(t *testing.T) {
	// Given: a schedule exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "g3")
	createSchedule(t, srv, "g3", "updatable", "rate(5 minutes)")

	// When: UpdateSchedule changes the expression
	resp := schDo(t, srv, http.MethodPut, "/schedules/updatable", map[string]any{
		"GroupName":          "g3",
		"ScheduleExpression": "rate(15 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 200 with updated ScheduleArn
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: GetSchedule returns the new expression
	get := schDo(t, srv, http.MethodGet, "/schedules/updatable?groupName=g3", nil)
	defer get.Body.Close()
	var result struct {
		ScheduleExpression string `json:"ScheduleExpression"`
	}
	helpers.DecodeJSON(t, get, &result)
	if result.ScheduleExpression != "rate(15 minutes)" {
		t.Errorf("expected updated expression, got %q", result.ScheduleExpression)
	}
}

func TestUpdateSchedule_replacesRatherThanMerges(t *testing.T) {
	// Given: a schedule carrying every optional member AWS lets a caller set
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "g3r")
	resp := schDo(t, srv, http.MethodPost, "/schedules/replaceable", map[string]any{
		"GroupName":                  "g3r",
		"ScheduleExpression":         "rate(5 minutes)",
		"ScheduleExpressionTimezone": "Europe/London",
		"Description":                "the original description",
		"KmsKeyArn":                  "arn:aws:kms:us-east-1:000000000000:key/original-key",
		"State":                      "DISABLED",
		"FlexibleTimeWindow":         map[string]any{"Mode": "FLEXIBLE", "MaximumWindowInMinutes": 15},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
			"Input":   `{"original":true}`,
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// And: GetSchedule reflects every one of them, KmsKeyArn included, before
	// the replacement below ever has a chance to clear it.
	preGet := schDo(t, srv, http.MethodGet, "/schedules/replaceable?groupName=g3r", nil)
	var preResult struct {
		KmsKeyArn string `json:"KmsKeyArn"`
	}
	helpers.DecodeJSON(t, preGet, &preResult)
	if preResult.KmsKeyArn != "arn:aws:kms:us-east-1:000000000000:key/original-key" {
		t.Fatalf("KmsKeyArn = %q, want it stored from CreateSchedule", preResult.KmsKeyArn)
	}

	// When: UpdateSchedule sends only the members AWS marks required
	resp = schDo(t, srv, http.MethodPut, "/schedules/replaceable", map[string]any{
		"GroupName":          "g3r",
		"ScheduleExpression": "rate(15 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:sqs:us-east-1:000000000000:q",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: every omitted member is unset, as AWS's full replacement leaves it
	get := schDo(t, srv, http.MethodGet, "/schedules/replaceable?groupName=g3r", nil)
	defer get.Body.Close()
	var result struct {
		ScheduleExpression         string `json:"ScheduleExpression"`
		ScheduleExpressionTimezone string `json:"ScheduleExpressionTimezone"`
		Description                string `json:"Description"`
		KmsKeyArn                  string `json:"KmsKeyArn"`
		State                      string `json:"State"`
		FlexibleTimeWindow         struct {
			Mode                   string `json:"Mode"`
			MaximumWindowInMinutes int    `json:"MaximumWindowInMinutes"`
		} `json:"FlexibleTimeWindow"`
		Target struct {
			Arn   string `json:"Arn"`
			Input string `json:"Input"`
		} `json:"Target"`
	}
	helpers.DecodeJSON(t, get, &result)

	if result.ScheduleExpression != "rate(15 minutes)" {
		t.Errorf("ScheduleExpression = %q, want rate(15 minutes)", result.ScheduleExpression)
	}
	if result.ScheduleExpressionTimezone != "" {
		t.Errorf("ScheduleExpressionTimezone = %q, want it cleared by the replacement", result.ScheduleExpressionTimezone)
	}
	if result.Description != "" {
		t.Errorf("Description = %q, want it cleared by the replacement", result.Description)
	}
	if result.KmsKeyArn != "" {
		t.Errorf("KmsKeyArn = %q, want it cleared by the replacement", result.KmsKeyArn)
	}
	if result.State != "ENABLED" {
		t.Errorf("State = %q, want ENABLED — an omitted State takes the default rather than the stored value", result.State)
	}
	if result.FlexibleTimeWindow.Mode != "OFF" || result.FlexibleTimeWindow.MaximumWindowInMinutes != 0 {
		t.Errorf("FlexibleTimeWindow = %+v, want {OFF 0}", result.FlexibleTimeWindow)
	}
	if result.Target.Input != "" {
		t.Errorf("Target.Input = %q, want it cleared by the replacement", result.Target.Input)
	}
	if result.Target.Arn != "arn:aws:sqs:us-east-1:000000000000:q" {
		t.Errorf("Target.Arn = %q, want the replacement's target", result.Target.Arn)
	}
}

func TestUpdateSchedule_keepsTheIdentityItWasCreatedWith(t *testing.T) {
	// Given: a schedule whose ARN and creation date a caller has already seen
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	createGroup(t, srv, "g3i")
	arn := createSchedule(t, srv, "g3i", "stable-identity", "rate(5 minutes)")

	get := schDo(t, srv, http.MethodGet, "/schedules/stable-identity?groupName=g3i", nil)
	var before struct {
		CreationDate         time.Time `json:"CreationDate"`
		LastModificationDate time.Time `json:"LastModificationDate"`
	}
	helpers.DecodeJSON(t, get, &before)
	get.Body.Close()

	srv.Clock.Add(2 * time.Second)

	// When: the schedule is replaced
	resp := schDo(t, srv, http.MethodPut, "/schedules/stable-identity", map[string]any{
		"GroupName":          "g3i",
		"ScheduleExpression": "rate(15 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: what is replaced is the schedule's content, not the schedule — its
	// ARN and creation date survive, and only the modification date moves
	get = schDo(t, srv, http.MethodGet, "/schedules/stable-identity?groupName=g3i", nil)
	defer get.Body.Close()
	var after struct {
		Arn                  string    `json:"Arn"`
		CreationDate         time.Time `json:"CreationDate"`
		LastModificationDate time.Time `json:"LastModificationDate"`
	}
	helpers.DecodeJSON(t, get, &after)

	if after.Arn != arn {
		t.Errorf("Arn = %q, want the ARN it was created with, %q", after.Arn, arn)
	}
	if !after.CreationDate.Equal(before.CreationDate) {
		t.Errorf("CreationDate = %s, want the original %s", after.CreationDate, before.CreationDate)
	}
	if !after.LastModificationDate.After(before.LastModificationDate) {
		t.Errorf("LastModificationDate = %s, want it moved past %s", after.LastModificationDate, before.LastModificationDate)
	}
}

func TestDeleteSchedule_success(t *testing.T) {
	// Given: a schedule exists
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "g4")
	createSchedule(t, srv, "g4", "del-sched", "rate(1 hour)")

	// When: DeleteSchedule is called
	resp := schDo(t, srv, http.MethodDelete, "/schedules/del-sched?groupName=g4", nil)
	resp.Body.Close()

	// Then: 200, subsequent Get returns 404
	helpers.AssertStatus(t, resp, http.StatusOK)
	get := schDo(t, srv, http.MethodGet, "/schedules/del-sched?groupName=g4", nil)
	defer get.Body.Close()
	helpers.AssertStatus(t, get, http.StatusNotFound)
}

func TestListSchedules_success(t *testing.T) {
	// Given: two schedules in the same group
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "g5")
	createSchedule(t, srv, "g5", "sched-x", "rate(1 minute)")
	createSchedule(t, srv, "g5", "sched-y", "rate(2 minutes)")

	// When: ListSchedules is called
	resp := schDo(t, srv, http.MethodGet, "/schedules?ScheduleGroup=g5", nil)
	defer resp.Body.Close()

	// Then: 200 with both schedules
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Schedules []struct {
			Name string `json:"Name"`
		} `json:"Schedules"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Schedules) != 2 {
		t.Errorf("expected 2 schedules, got %d", len(result.Schedules))
	}
}

func TestListSchedules_filterByGroup(t *testing.T) {
	// Given: schedules in different groups
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "grp-a")
	createGroup(t, srv, "grp-b")
	createSchedule(t, srv, "grp-a", "a-sched", "rate(1 minute)")
	createSchedule(t, srv, "grp-b", "b-sched", "rate(1 minute)")

	// When: ListSchedules is filtered by grp-a
	resp := schDo(t, srv, http.MethodGet, "/schedules?ScheduleGroup=grp-a", nil)
	defer resp.Body.Close()

	// Then: only grp-a schedules are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Schedules []struct {
			Name      string `json:"Name"`
			GroupName string `json:"GroupName"`
		} `json:"Schedules"`
	}
	helpers.DecodeJSON(t, resp, &result)
	if len(result.Schedules) != 1 {
		t.Errorf("expected 1 schedule, got %d", len(result.Schedules))
	}
	if result.Schedules[0].Name != "a-sched" {
		t.Errorf("expected a-sched, got %q", result.Schedules[0].Name)
	}
}

func TestCreateSchedule_cronExpression(t *testing.T) {
	// Given: a schedule group
	srv := helpers.NewTestServer(t)
	createGroup(t, srv, "cron-group")

	// When: CreateSchedule with AWS cron expression
	resp := schDo(t, srv, http.MethodPost, "/schedules/cron-sched", map[string]any{
		"GroupName":          "cron-group",
		"ScheduleExpression": "cron(0 12 * * ? *)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
		},
	})
	defer resp.Body.Close()

	// Then: 200 — cron expression is accepted and stored
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// ─── Target Firing Tests ──────────────────────────────────────────────────────

func TestSchedule_rateFiresTarget(t *testing.T) {
	// Given: a server with mock clock and a rate schedule targeting an SQS queue
	srv := helpers.NewTestServer(t, helpers.WithMockClock())

	// Create an SQS queue to receive scheduled events
	createReq, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/?Action=CreateQueue&QueueName=sched-target", nil)
	createResp, _ := http.DefaultClient.Do(createReq)
	createResp.Body.Close()

	// Create a schedule targeting that SQS queue with a 5-minute rate
	resp := schDo(t, srv, http.MethodPost, "/schedules/my-fire-sched", map[string]any{
		"ScheduleExpression": "rate(5 minutes)",
		"FlexibleTimeWindow": map[string]any{"Mode": "OFF"},
		"Target": map[string]any{
			"Arn":     "arn:aws:sqs:us-east-1:000000000000:sched-target",
			"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role",
			"Input":   `{"event":"scheduled"}`,
		},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// When: advance mock clock by 6 minutes (past the 5-minute rate)
	srv.Clock.Add(6 * time.Minute)
	time.Sleep(200 * time.Millisecond) // let background goroutine fire

	// Then: the SQS queue should have received a message
	// Then: verify we can still GET the schedule (engine didn't crash)
	getResp := schDo(t, srv, http.MethodGet, "/schedules/my-fire-sched", nil)
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)
}
