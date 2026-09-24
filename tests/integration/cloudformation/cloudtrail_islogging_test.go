package cloudformation_test

// cloudtrail_islogging_test.go — #1754: CDK's Trail construct sets IsLogging,
// and CreateTrail always starts a new trail with logging off (matching real
// AWS), so the property has to ride a follow-up StartLogging/StopLogging call
// or GetTrailStatus disagrees with the template on the very first deploy.
// Covers both Create (IsLogging: true reaching StartLogging) and Update
// (toggling the flag in place, without replacing the trail).

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func getCloudTrailStatus(t *testing.T, srv *helpers.TestServer, name string) bool {
	t.Helper()
	resp := cloudtrailJSONCall(t, srv, "GetTrailStatus", map[string]any{"Name": name})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		IsLogging bool `json:"IsLogging"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode GetTrailStatus: %v", err)
	}
	return result.IsLogging
}

func cloudtrailIsLoggingTemplate(trailName string, isLogging bool) string {
	logging := "false"
	if isLogging {
		logging = "true"
	}
	return `{
  "Resources": {
    "Trail": {
      "Type": "AWS::CloudTrail::Trail",
      "Properties": {
        "TrailName": "` + trailName + `",
        "S3BucketName": "cloudtrail-islogging-bucket",
        "IsLogging": ` + logging + `
      }
    }
  }
}`
}

// TestCreateStack_CloudTrailIsLogging_startsLogging asserts that a template
// asking for IsLogging: true reaches StartLogging on Create, so
// GetTrailStatus agrees with the template on the first deploy rather than
// reporting the CreateTrail default (always off).
func TestCreateStack_CloudTrailIsLogging_startsLogging(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "cloudtrail-islogging-create-stack"
	const trailName = "cfn-islogging-create-trail"

	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {cloudtrailIsLoggingTemplate(trailName, true)},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	if !getCloudTrailStatus(t, srv, trailName) {
		t.Fatalf("GetTrailStatus(%q).IsLogging = false, want true after IsLogging: true", trailName)
	}
}

// TestCreateStack_CloudTrailIsLogging_defaultsToNotLogging asserts that a
// template omitting IsLogging (or setting it false) leaves the trail exactly
// where CreateTrail puts every new trail — not logging — with no spurious
// StartLogging call.
func TestCreateStack_CloudTrailIsLogging_defaultsToNotLogging(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "cloudtrail-islogging-default-stack"
	const trailName = "cfn-islogging-default-trail"

	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {cloudtrailIsLoggingTemplate(trailName, false)},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	if getCloudTrailStatus(t, srv, trailName) {
		t.Fatalf("GetTrailStatus(%q).IsLogging = true, want false with IsLogging: false", trailName)
	}
}

// TestUpdateStack_CloudTrailIsLogging_toggles asserts that a stack update
// changing IsLogging calls StartLogging/StopLogging in place — the trail is
// not replaced (TrailName is unchanged, so the physical ID must stay put) —
// and that both directions (off→on and on→off) work.
func TestUpdateStack_CloudTrailIsLogging_toggles(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "cloudtrail-islogging-update-stack"
	const trailName = "cfn-islogging-update-trail"

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {cloudtrailIsLoggingTemplate(trailName, false)},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")
	if getCloudTrailStatus(t, srv, trailName) {
		t.Fatalf("trail logging before update should be false")
	}
	originalPhysicalID := describeStackResourceIDs(t, srv, stackName)["Trail"]

	// off -> on
	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {cloudtrailIsLoggingTemplate(trailName, true)},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")
	if !getCloudTrailStatus(t, srv, trailName) {
		t.Fatalf("GetTrailStatus.IsLogging = false after updating IsLogging to true, want true")
	}
	if got := describeStackResourceIDs(t, srv, stackName)["Trail"]; got != originalPhysicalID {
		t.Fatalf("Trail physical ID changed from %q to %q on an IsLogging-only update; trail should not have been replaced", originalPhysicalID, got)
	}

	// on -> off
	updateResp2 := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {cloudtrailIsLoggingTemplate(trailName, false)},
	})
	defer updateResp2.Body.Close()
	helpers.AssertStatus(t, updateResp2, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")
	if getCloudTrailStatus(t, srv, trailName) {
		t.Fatalf("GetTrailStatus.IsLogging = true after updating IsLogging to false, want false")
	}
}
