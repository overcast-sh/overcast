package cloudformation_test

// rds_managed_password_test.go — ManageMasterUserPassword on
// AWS::RDS::DBInstance and AWS::RDS::DBCluster (#544). The provisioner used
// to drop the property entirely: a template setting ManageMasterUserPassword
// with no MasterUserPassword left the container started with an empty
// password, and Fn::GetAtt MasterUserSecret.SecretArn resolved to nothing.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// rdsQueryForCFN sends an RDS Query protocol request against the same test
// server the stack was deployed to — package-local because rds_test's own
// rdsQuery lives in a different (_test) package this one cannot import.
func rdsQueryForCFN(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", "2014-10-31")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(params.Encode()))
	if err != nil {
		t.Fatalf("rdsQueryForCFN: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("rdsQueryForCFN %s: %v", action, err)
	}
	return resp
}

// rdsManagedInstanceTemplate builds a one-instance template that asks RDS to
// manage the master password itself, and exposes the resulting secret ARN as
// a stack Output the way a real template's Fn::GetAtt would.
func rdsManagedInstanceTemplate(id string) string {
	idProp := ""
	if id != "" {
		idProp = `"DBInstanceIdentifier": ` + `"` + id + `",`
	}
	return `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Database": {
      "Type": "AWS::RDS::DBInstance",
      "Properties": {
        ` + idProp + `
        "Engine": "mysql",
        "DBInstanceClass": "db.t3.micro",
        "AllocatedStorage": "20",
        "MasterUsername": "admin",
        "ManageMasterUserPassword": true,
        "Tags": [{"Key": "team", "Value": "platform"}]
      }
    }
  },
  "Outputs": {
    "SecretArn": { "Value": { "Fn::GetAtt": ["Database", "MasterUserSecret.SecretArn"] } }
  }
}`
}

// A template that deploys with ManageMasterUserPassword must reach
// CREATE_COMPLETE, and Fn::GetAtt MasterUserSecret.SecretArn must resolve to
// a real secret — the GetAtt this issue's Definition of Done names explicitly.
func TestCreateStack_rdsManagedMasterPasswordDeploysAndGetAttResolves(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "rds-managed-password-stack"
	const instanceID = "cfn-managed-instance"

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{rdsManagedInstanceTemplate(instanceID)},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	secretArn := stackOutput(t, srv, stackName, "SecretArn")
	if secretArn == "" {
		t.Fatal("Fn::GetAtt MasterUserSecret.SecretArn resolved to an empty string")
	}
	if !strings.Contains(secretArn, ":secret:rds!db-") {
		t.Errorf("resolved secret ARN %q does not follow AWS's rds!db-<uuid> naming", secretArn)
	}

	// DescribeDBInstances has to carry the same secret — the GetAtt is not
	// allowed to be the only place it shows up.
	dr := rdsQueryForCFN(t, srv, "DescribeDBInstances", url.Values{"DBInstanceIdentifier": []string{instanceID}})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
	body := readBody(t, dr)
	if !strings.Contains(string(body), "<SecretArn>"+secretArn+"</SecretArn>") {
		t.Errorf("DescribeDBInstances does not carry the resolved secret ARN %q; body: %s", secretArn, body)
	}

	// Tags forwarding (#544 priority 2): AddTagsToResource/ListTagsForResource
	// already existed and were simply never called by this handler.
	tr := rdsQueryForCFN(t, srv, "ListTagsForResource", url.Values{
		"ResourceName": []string{"arn:aws:rds:" + srv.Config.Region + ":" + srv.Config.AccountID + ":db:" + instanceID},
	})
	defer tr.Body.Close()
	helpers.AssertStatus(t, tr, http.StatusOK)
	tagsBody := string(readBody(t, tr))
	if !strings.Contains(tagsBody, "<Key>team</Key>") || !strings.Contains(tagsBody, "<Value>platform</Value>") {
		t.Errorf("template Tags were not forwarded to the DB instance; ListTagsForResource body: %s", tagsBody)
	}
}

// A template that asks for both ManageMasterUserPassword and a literal
// MasterUserPassword is invalid on AWS, and the stack must fail rather than
// silently pick one.
func TestCreateStack_rdsManagedMasterPasswordCombinationFails(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "rds-managed-password-conflict-stack"

	tmpl := `{
  "AWSTemplateFormatVersion": "2010-09-09",
  "Resources": {
    "Database": {
      "Type": "AWS::RDS::DBInstance",
      "Properties": {
        "Engine": "mysql",
        "DBInstanceClass": "db.t3.micro",
        "AllocatedStorage": "20",
        "MasterUsername": "admin",
        "MasterUserPassword": "explicit-password-1",
        "ManageMasterUserPassword": true
      }
    }
  }
}`

	cr := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    []string{stackName},
		"TemplateBody": []string{tmpl},
	})
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)

	got := waitForStackStatusIn(t, srv, stackName, "CREATE_COMPLETE", "ROLLBACK_COMPLETE", "CREATE_FAILED")
	if got == "CREATE_COMPLETE" {
		t.Fatalf("stack reported CREATE_COMPLETE for a template combining MasterUserPassword and ManageMasterUserPassword=true")
	}
}
