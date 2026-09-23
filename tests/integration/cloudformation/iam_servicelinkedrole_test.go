package cloudformation_test

// Integration coverage for #1981: AWS::IAM::ServiceLinkedRole derived its role
// name from the service prefix ("AWSServiceRoleFor" + everything before the
// AWSServiceName's first '.'), which turned
// elasticloadbalancing.amazonaws.com into AWSServiceRoleForelasticloadbalancing
// rather than AWS's real name, AWSServiceRoleForElasticLoadBalancing. A
// template or a later GetRole/AssumeRole call against the real AWS name found
// nothing Overcast had created.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// TestCreateStack_IAMServiceLinkedRole_usesAWSsPerServiceRoleName pins the
// per-service table against AWS's documented service-linked role names
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_aws-services-that-work-with-iam.html),
// for the services the #1981 acceptance criteria names "at minimum".
func TestCreateStack_IAMServiceLinkedRole_usesAWSsPerServiceRoleName(t *testing.T) {
	for _, tc := range []struct {
		serviceName string
		roleName    string
	}{
		{"elasticloadbalancing.amazonaws.com", "AWSServiceRoleForElasticLoadBalancing"},
		{"autoscaling.amazonaws.com", "AWSServiceRoleForAutoScaling"},
		{"ecs.amazonaws.com", "AWSServiceRoleForECS"},
		{"eks.amazonaws.com", "AWSServiceRoleForAmazonEKS"},
		{"rds.amazonaws.com", "AWSServiceRoleForRDS"},
		{"elasticache.amazonaws.com", "AWSServiceRoleForElastiCache"},
		{"replicator.lambda.amazonaws.com", "AWSServiceRoleForLambdaReplicator"},
		{"opensearchservice.amazonaws.com", "AWSServiceRoleForAmazonOpenSearchService"},
		{"organizations.amazonaws.com", "AWSServiceRoleForOrganizations"},
	} {
		t.Run(tc.serviceName, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			stackName := "slr-" + strings.ReplaceAll(strings.Split(tc.serviceName, ".")[0], "_", "-")
			template := `{"Resources": {"Role": {"Type": "AWS::IAM::ServiceLinkedRole", "Properties": {
        "AWSServiceName": "` + tc.serviceName + `"}}}}`

			create := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
			defer create.Body.Close()
			helpers.AssertStatus(t, create, http.StatusOK)
			status := waitForStackStatusIn(t, srv, stackName,
				"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
			if status != "CREATE_COMPLETE" {
				t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
					status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
			}

			// Ref is the ARN (AWS's own documented behaviour for this
			// resource), and it carries the real role name at its end.
			physID := describeStackResourceIDs(t, srv, stackName)["Role"]
			if !strings.HasSuffix(physID, "/"+tc.roleName) {
				t.Errorf("Ref = %q, want it to end with /%s", physID, tc.roleName)
			}

			// And: GetRole under the real AWS name finds it — the name a
			// later template or an AssumeRole call would actually use.
			resp := iamQuery(t, srv, "GetRole", url.Values{"RoleName": {tc.roleName}})
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)
		})
	}
}

// TestCreateStack_IAMServiceLinkedRole_customSuffix pins CustomSuffix: AWS
// appends it to the per-service role name with an underscore, for the
// services that allow more than one linked role.
func TestCreateStack_IAMServiceLinkedRole_customSuffix(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "slr-custom-suffix"
	template := `{"Resources": {"Role": {"Type": "AWS::IAM::ServiceLinkedRole", "Properties": {
    "AWSServiceName": "elasticloadbalancing.amazonaws.com",
    "CustomSuffix": "myapp"}}}}`

	create := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	status := waitForStackStatusIn(t, srv, stackName,
		"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
	if status != "CREATE_COMPLETE" {
		t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
			status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
	}

	resp := iamQuery(t, srv, "GetRole", url.Values{"RoleName": {"AWSServiceRoleForElasticLoadBalancing_myapp"}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

// TestCreateStack_IAMServiceLinkedRole_unknownServiceFallsBackToATitleCasedGuess
// pins the other half: a service outside the table must not fail the stack —
// that would regress a stack that deploys today — so it falls back to a
// title-cased derivation of the service principal's first label instead. The
// fallback is only a guess (it will not always match AWS's real name, the way
// EKS's "AmazonEKS" would not fall out of this derivation either), which is
// why serviceLinkedRoleNames exists: a service found on the fallback belongs
// added to that table with its documented name.
func TestCreateStack_IAMServiceLinkedRole_unknownServiceFallsBackToATitleCasedGuess(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "slr-unknown-service"
	const wantRoleName = "AWSServiceRoleForNot-a-real-service"
	template := `{"Resources": {"Role": {"Type": "AWS::IAM::ServiceLinkedRole", "Properties": {
    "AWSServiceName": "not-a-real-service.amazonaws.com"}}}}`

	create := cfnQuery(t, srv, "CreateStack", url.Values{"StackName": {stackName}, "TemplateBody": {template}})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)
	status := waitForStackStatusIn(t, srv, stackName,
		"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
	if status != "CREATE_COMPLETE" {
		t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
			status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
	}

	physID := describeStackResourceIDs(t, srv, stackName)["Role"]
	if !strings.HasSuffix(physID, "/"+wantRoleName) {
		t.Errorf("Ref = %q, want it to end with /%s", physID, wantRoleName)
	}

	resp := iamQuery(t, srv, "GetRole", url.Values{"RoleName": {wantRoleName}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}
