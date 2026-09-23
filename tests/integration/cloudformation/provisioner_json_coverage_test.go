package cloudformation_test

// provisioner_json_coverage_test.go — #2060: the tail pass #540 asked for over
// the metadata-service handlers in provisioner_json_coverage.go. None of them
// called noteUnconsumedProperties, so every property the backing Overcast
// service already implements but the handler never read was dropped in
// silence — the stack still reported CREATE_COMPLETE. Each test below sets a
// property from that list and proves it reached the service through the
// service's own Describe/Get/ListTags, never through CloudFormation's own
// bookkeeping.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ── AWS::CloudTrail::Trail ──────────────────────────────────────────────────

func TestCreateStack_CloudTrailTrail_optionalPropertiesForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "cloudtrail-json-coverage-stack"
	const trailName = "cfn-json-coverage-trail"
	template := `{
  "Resources": {
    "Trail": {
      "Type": "AWS::CloudTrail::Trail",
      "Properties": {
        "TrailName": "` + trailName + `",
        "S3BucketName": "cloudtrail-json-coverage-bucket",
        "S3KeyPrefix": "prefix/path",
        "CloudWatchLogsLogGroupArn": "arn:aws:logs:us-east-1:000000000000:log-group:trail-lg:*",
        "CloudWatchLogsRoleArn": "arn:aws:iam::000000000000:role/trail-cwl-role",
        "EnableLogFileValidation": true,
        "KMSKeyId": "arn:aws:kms:us-east-1:000000000000:key/trail-key",
        "IsOrganizationTrail": true
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	resp := cloudtrailJSONCall(t, srv, "DescribeTrails", map[string]any{"trailNameList": []string{trailName}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		TrailList []struct {
			Name                      string `json:"Name"`
			S3KeyPrefix               string `json:"S3KeyPrefix"`
			CloudWatchLogsLogGroupArn string `json:"CloudWatchLogsLogGroupArn"`
			CloudWatchLogsRoleArn     string `json:"CloudWatchLogsRoleArn"`
			LogFileValidationEnabled  bool   `json:"LogFileValidationEnabled"`
			KmsKeyId                  string `json:"KmsKeyId"`
			IsOrganizationTrail       bool   `json:"IsOrganizationTrail"`
		} `json:"trailList"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode DescribeTrails: %v", err)
	}
	if len(result.TrailList) != 1 {
		t.Fatalf("DescribeTrails returned %d trails, want 1: %#v", len(result.TrailList), result.TrailList)
	}
	got := result.TrailList[0]
	if got.S3KeyPrefix != "prefix/path" {
		t.Errorf("S3KeyPrefix = %q, want %q", got.S3KeyPrefix, "prefix/path")
	}
	if got.CloudWatchLogsLogGroupArn != "arn:aws:logs:us-east-1:000000000000:log-group:trail-lg:*" {
		t.Errorf("CloudWatchLogsLogGroupArn = %q", got.CloudWatchLogsLogGroupArn)
	}
	if got.CloudWatchLogsRoleArn != "arn:aws:iam::000000000000:role/trail-cwl-role" {
		t.Errorf("CloudWatchLogsRoleArn = %q", got.CloudWatchLogsRoleArn)
	}
	if !got.LogFileValidationEnabled {
		t.Errorf("LogFileValidationEnabled = %v, want true (from EnableLogFileValidation)", got.LogFileValidationEnabled)
	}
	if got.KmsKeyId != "arn:aws:kms:us-east-1:000000000000:key/trail-key" {
		t.Errorf("KmsKeyId = %q, want the KMSKeyId property's value", got.KmsKeyId)
	}
	if !got.IsOrganizationTrail {
		t.Errorf("IsOrganizationTrail = %v, want true", got.IsOrganizationTrail)
	}
}

// ── AWS::AppConfig::Application / Environment / ConfigurationProfile ───────

func TestCreateStack_AppConfig_tagsMonitorsAndValidatorsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "appconfig-json-coverage-stack"
	template := `{
  "Resources": {
    "App": {
      "Type": "AWS::AppConfig::Application",
      "Properties": {
        "Name": "jc-app",
        "Tags": [{"Key": "team", "Value": "platform"}]
      }
    },
    "Env": {
      "Type": "AWS::AppConfig::Environment",
      "Properties": {
        "ApplicationId": {"Ref": "App"},
        "Name": "jc-env",
        "Monitors": [{"AlarmArn": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:jc-alarm", "AlarmRoleArn": "arn:aws:iam::000000000000:role/jc-monitor-role"}],
        "Tags": [{"Key": "team", "Value": "platform"}]
      }
    },
    "Profile": {
      "Type": "AWS::AppConfig::ConfigurationProfile",
      "Properties": {
        "ApplicationId": {"Ref": "App"},
        "Name": "jc-profile",
        "LocationUri": "hosted",
        "Description": "a profile with everything set",
        "RetrievalRoleArn": "arn:aws:iam::000000000000:role/jc-retrieval-role",
        "Type": "AWS.Freeform",
        "Validators": [{"Type": "LAMBDA", "Content": "arn:aws:lambda:us-east-1:000000000000:function:jc-validator"}],
        "Tags": [{"Key": "team", "Value": "platform"}]
      }
    }
  },
  "Outputs": {
    "ApplicationId": {"Value": {"Ref": "App"}},
    "EnvironmentId": {"Value": {"Fn::GetAtt": ["Env", "Id"]}},
    "ProfileId": {"Value": {"Fn::GetAtt": ["Profile", "Id"]}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputs := describeStackOutputs(t, srv, stackName)
	appID := outputs["ApplicationId"]
	envID := outputs["EnvironmentId"]
	profID := outputs["ProfileId"]

	// Application Tags, via ListTagsForResource against the application ARN.
	appARN := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s", appID)
	if tags := appconfigListTags(t, srv, appARN); tags["team"] != "platform" {
		t.Errorf("application tags = %#v, want team=platform", tags)
	}

	// Environment Monitors and Tags, via GetEnvironment and ListTagsForResource.
	_, env := appconfigGet(t, srv, fmt.Sprintf("/applications/%s/environments/%s", appID, envID))
	monitors, _ := env["Monitors"].([]any)
	if len(monitors) != 1 {
		t.Fatalf("GetEnvironment Monitors = %#v, want 1 entry", env["Monitors"])
	}
	monitor, _ := monitors[0].(map[string]any)
	if monitor["AlarmArn"] != "arn:aws:cloudwatch:us-east-1:000000000000:alarm:jc-alarm" {
		t.Errorf("Monitor.AlarmArn = %v", monitor["AlarmArn"])
	}
	if monitor["AlarmRoleArn"] != "arn:aws:iam::000000000000:role/jc-monitor-role" {
		t.Errorf("Monitor.AlarmRoleArn = %v", monitor["AlarmRoleArn"])
	}
	envARN := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s/environment/%s", appID, envID)
	if tags := appconfigListTags(t, srv, envARN); tags["team"] != "platform" {
		t.Errorf("environment tags = %#v, want team=platform", tags)
	}

	// ConfigurationProfile Description/RetrievalRoleArn/Type/Validators/Tags,
	// via GetConfigurationProfile and ListTagsForResource.
	_, prof := appconfigGet(t, srv, fmt.Sprintf("/applications/%s/configurationprofiles/%s", appID, profID))
	if prof["Description"] != "a profile with everything set" {
		t.Errorf("Profile.Description = %v", prof["Description"])
	}
	if prof["RetrievalRoleArn"] != "arn:aws:iam::000000000000:role/jc-retrieval-role" {
		t.Errorf("Profile.RetrievalRoleArn = %v", prof["RetrievalRoleArn"])
	}
	if prof["Type"] != "AWS.Freeform" {
		t.Errorf("Profile.Type = %v", prof["Type"])
	}
	validators, _ := prof["Validators"].([]any)
	if len(validators) != 1 {
		t.Fatalf("Profile.Validators = %#v, want 1 entry", prof["Validators"])
	}
	validator, _ := validators[0].(map[string]any)
	if validator["Type"] != "LAMBDA" || validator["Content"] != "arn:aws:lambda:us-east-1:000000000000:function:jc-validator" {
		t.Errorf("Validator = %#v", validator)
	}
	profARN := fmt.Sprintf("arn:aws:appconfig:us-east-1:000000000000:application/%s/configurationprofile/%s", appID, profID)
	if tags := appconfigListTags(t, srv, profARN); tags["team"] != "platform" {
		t.Errorf("configuration profile tags = %#v, want team=platform", tags)
	}
}

// appconfigListTags reads an AppConfig resource's tags through
// ListTagsForResource (GET /tags/{ResourceArn}), signed with AppConfig's
// credential scope the same way appconfigGet is.
func appconfigListTags(t *testing.T, srv *helpers.TestServer, arn string) map[string]string {
	t.Helper()
	status, body := appconfigGet(t, srv, "/tags/"+url.PathEscape(arn))
	if status != http.StatusOK {
		t.Fatalf("ListTagsForResource(%s): HTTP %d: %#v", arn, status, body)
	}
	tags, _ := body["Tags"].(map[string]any)
	out := make(map[string]string, len(tags))
	for k, v := range tags {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// ── AWS::OpenSearchService::Domain ──────────────────────────────────────────

func opensearchCFNRequest(t *testing.T, srv *helpers.TestServer, method, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20250101/us-east-1/es/aws4_request, SignedHeaders=host, Signature=fake")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestCreateStack_OpenSearchDomain_tagsAndClusterConfigForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "opensearch-json-coverage-stack"
	const domainName = "jc-domain"
	template := `{
  "Resources": {
    "Domain": {
      "Type": "AWS::OpenSearchService::Domain",
      "Properties": {
        "DomainName": "` + domainName + `",
        "ClusterConfig": {"InstanceType": "r6g.large.search", "InstanceCount": 3},
        "Tags": [{"Key": "team", "Value": "search"}]
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	resp := opensearchCFNRequest(t, srv, http.MethodGet, "/2021-01-01/opensearch/domain/"+domainName)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		DomainStatus struct {
			ARN           string         `json:"ARN"`
			ClusterConfig map[string]any `json:"ClusterConfig"`
		} `json:"DomainStatus"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode DescribeDomain: %v", err)
	}
	if got, _ := result.DomainStatus.ClusterConfig["InstanceType"].(string); got != "r6g.large.search" {
		t.Errorf("ClusterConfig.InstanceType = %q, want %q (ClusterConfig: %#v)", got, "r6g.large.search", result.DomainStatus.ClusterConfig)
	}
	if got, _ := result.DomainStatus.ClusterConfig["InstanceCount"].(float64); got != 3 {
		t.Errorf("ClusterConfig.InstanceCount = %v, want 3", result.DomainStatus.ClusterConfig["InstanceCount"])
	}

	tagsResp := opensearchCFNRequest(t, srv, http.MethodGet, "/2021-01-01/tags?arn="+url.QueryEscape(result.DomainStatus.ARN))
	defer tagsResp.Body.Close()
	helpers.AssertStatus(t, tagsResp, http.StatusOK)
	var tagsResult struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	if err := json.NewDecoder(tagsResp.Body).Decode(&tagsResult); err != nil {
		t.Fatalf("decode ListTags: %v", err)
	}
	found := false
	for _, tag := range tagsResult.TagList {
		if tag.Key == "team" && tag.Value == "search" {
			found = true
		}
	}
	if !found {
		t.Errorf("domain tags = %#v, want team=search", tagsResult.TagList)
	}
}

// ── AWS::CertificateManager::Certificate ────────────────────────────────────

func acmJSONCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	return awsJSONCall(t, srv, "CertificateManager.", action, "application/x-amz-json-1.1", body)
}

func TestCreateStack_ACMCertificate_validationMethodAndTagsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "acm-json-coverage-stack"
	template := `{
  "Resources": {
    "Cert": {
      "Type": "AWS::CertificateManager::Certificate",
      "Properties": {
        "DomainName": "jc.example.com",
        "ValidationMethod": "DNS",
        "Tags": [{"Key": "team", "Value": "edge"}]
      }
    }
  },
  "Outputs": {
    "CertArn": {"Value": {"Ref": "Cert"}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	arn := describeStackOutputs(t, srv, stackName)["CertArn"]

	descResp := acmJSONCall(t, srv, "DescribeCertificate", map[string]any{"CertificateArn": arn})
	defer descResp.Body.Close()
	helpers.AssertStatus(t, descResp, http.StatusOK)
	var desc struct {
		Certificate struct {
			DomainValidationOptions []struct {
				DomainName       string `json:"DomainName"`
				ValidationMethod string `json:"ValidationMethod"`
			} `json:"DomainValidationOptions"`
		} `json:"Certificate"`
	}
	if err := json.NewDecoder(descResp.Body).Decode(&desc); err != nil {
		t.Fatalf("decode DescribeCertificate: %v", err)
	}
	if len(desc.Certificate.DomainValidationOptions) != 1 || desc.Certificate.DomainValidationOptions[0].ValidationMethod != "DNS" {
		t.Errorf("DomainValidationOptions = %#v, want one entry with ValidationMethod=DNS", desc.Certificate.DomainValidationOptions)
	}

	tagsResp := acmJSONCall(t, srv, "ListTagsForCertificate", map[string]any{"CertificateArn": arn})
	defer tagsResp.Body.Close()
	helpers.AssertStatus(t, tagsResp, http.StatusOK)
	var tagsResult struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if err := json.NewDecoder(tagsResp.Body).Decode(&tagsResult); err != nil {
		t.Fatalf("decode ListTagsForCertificate: %v", err)
	}
	found := false
	for _, tag := range tagsResult.Tags {
		if tag.Key == "team" && tag.Value == "edge" {
			found = true
		}
	}
	if !found {
		t.Errorf("certificate tags = %#v, want team=edge", tagsResult.Tags)
	}
}

// ── AWS::Athena::WorkGroup ──────────────────────────────────────────────────

func TestCreateStack_AthenaWorkGroup_tagsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "athena-json-coverage-stack"
	const wgName = "jc-workgroup"
	template := `{
  "Resources": {
    "WorkGroup": {
      "Type": "AWS::Athena::WorkGroup",
      "Properties": {
        "Name": "` + wgName + `",
        "Tags": [{"Key": "team", "Value": "analytics"}]
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	arn := fmt.Sprintf("arn:aws:athena:us-east-1:000000000000:workgroup/%s", wgName)
	resp := athenaJSONCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode ListTagsForResource: %v", err)
	}
	found := false
	for _, tag := range result.Tags {
		if tag.Key == "team" && tag.Value == "analytics" {
			found = true
		}
	}
	if !found {
		t.Errorf("workgroup tags = %#v, want team=analytics", result.Tags)
	}
}

// ── AWS::Shield::Protection ─────────────────────────────────────────────────

func shieldJSONCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	return awsJSONCall(t, srv, "AWSShield_20160616.", action, "application/x-amz-json-1.1", body)
}

func TestCreateStack_ShieldProtection_tagsForwarded(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "shield-json-coverage-stack"
	template := `{
  "Resources": {
    "Protection": {
      "Type": "AWS::Shield::Protection",
      "Properties": {
        "Name": "jc-protection",
        "ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/jc-lb/abc123",
        "Tags": [{"Key": "team", "Value": "netsec"}]
      }
    }
  },
  "Outputs": {
    "ProtectionId": {"Value": {"Ref": "Protection"}}
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	protectionID := describeStackOutputs(t, srv, stackName)["ProtectionId"]
	arn := fmt.Sprintf("arn:aws:shield::000000000000:protection/%s", protectionID)

	resp := shieldJSONCall(t, srv, "ListTagsForResource", map[string]any{"ResourceARN": arn})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode ListTagsForResource: %v", err)
	}
	found := false
	for _, tag := range result.Tags {
		if tag.Key == "team" && tag.Value == "netsec" {
			found = true
		}
	}
	if !found {
		t.Errorf("protection tags = %#v, want team=netsec", result.Tags)
	}
}

// ── AWS::Transfer::User ──────────────────────────────────────────────────────

func TestCreateStack_TransferUser_homeDirectoryAndPolicyForwardedOnCreate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "transfer-user-json-coverage-stack"
	const policy = `{"Version":"2012-10-17","Statement":[]}`
	template := `{
  "Resources": {
    "Server": {
      "Type": "AWS::Transfer::Server",
      "Properties": {}
    },
    "User": {
      "Type": "AWS::Transfer::User",
      "Properties": {
        "ServerId": {"Ref": "Server"},
        "UserName": "jc-user",
        "Role": "arn:aws:iam::000000000000:role/jc-transfer-role",
        "HomeDirectory": "/jc-bucket/home",
        "Policy": ` + fmt.Sprintf("%q", policy) + `
      }
    }
  }
}`

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	serverID := describeStackResourceIDs(t, srv, stackName)["Server"]

	resp := transferJSONCall(t, srv, "DescribeUser", map[string]any{"ServerId": serverID, "UserName": "jc-user"})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		User struct {
			HomeDirectory string `json:"HomeDirectory"`
			Policy        string `json:"Policy"`
		} `json:"User"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode DescribeUser: %v", err)
	}
	if result.User.HomeDirectory != "/jc-bucket/home" {
		t.Errorf("User.HomeDirectory = %q, want %q", result.User.HomeDirectory, "/jc-bucket/home")
	}
	if result.User.Policy != policy {
		t.Errorf("User.Policy = %q, want %q", result.User.Policy, policy)
	}
}

// ── AWS::SES::ConfigurationSet ──────────────────────────────────────────────

func TestCreateStack_SESConfigurationSet_failsHonestly(t *testing.T) {
	// Given: SES's own CreateConfigurationSet is an honest 501 (SES v1's stub,
	// see ses/handler_stubs.go and ses/capabilities_dev.go). Before this fix
	// the resource ran under stubResourceHandler, which fabricates a physical
	// ID and never calls SES at all, so the stack reported CREATE_COMPLETE for
	// a resource that does not exist anywhere.
	srv := helpers.NewTestServer(t)
	const stackName = "ses-configurationset-json-coverage-stack"
	template := `{
  "Resources": {
    "ConfigSet": {
      "Type": "AWS::SES::ConfigurationSet",
      "Properties": {
        "Name": "jc-configuration-set"
      }
    }
  }
}`

	// DisableRollback=true so the stack settles on CREATE_FAILED directly,
	// rather than ROLLBACK_COMPLETE, making the assertion below simpler.
	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":       {stackName},
		"TemplateBody":    {template},
		"DisableRollback": {"true"},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_FAILED")

	eventsResp := cfnQuery(t, srv, "DescribeStackEvents", url.Values{
		"StackName": {stackName},
	})
	defer eventsResp.Body.Close()
	body := string(readBody(t, eventsResp))
	for _, want := range []string{"ResourceStatusReason", "CreateConfigurationSet", "501"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in stack events, got: %s", want, body)
		}
	}
}
