package cloudformation

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// absentAnswer is what one service replies while the resource a teardown names
// is not there.
type absentAnswer func(r *http.Request) (code int, body string)

// gone builds the single-reply case: every call this teardown makes is answered
// the same way, because the resource is absent at every step of it.
func gone(code int, body string) absentAnswer {
	return func(*http.Request) (int, string) { return code, body }
}

// absentCase is one resource type whose teardown has to survive the resource
// already being gone.
//
// Each answer here is the one the emulator's own service gives — status and
// body verbatim — captured by dispatching that service's real delete against a
// live emulator router for an identifier that was never created. They are
// recorded rather than invented because the shape varies per service and the
// classifier reads it: API Gateway and EKS answer HTTP 404, EC2 and RDS answer
// HTTP 400 with the absence named in a Query-XML Code, WAFv2 spells it
// "Nonexistent", and ECS's task definition does not name it at all.
type absentCase struct {
	resType    string
	physicalID string
	answer     absentAnswer
}

// A resource that is merely already gone must never fail a teardown. Rollback
// treats any delete error as a resource still standing — DELETE_FAILED on the
// resource, ROLLBACK_FAILED on the stack — so a handler that reports its
// dispatch error raw strands a stack over a resource nobody needs deleted.
//
// This is the other half of the rule teardownError states, and the case for
// each type is driven through the registered handler rather than the helper so
// that a handler which never routes its dispatch through the helper fails here.
func TestResourceDelete_absentResourceIsASuccessfulTeardown(t *testing.T) {
	const (
		apigwNotFound   = `{"__type":"NotFoundException","message":"Invalid REST API identifier specified: absent"}`
		appsyncNotFound = `{"__type":"NotFoundException","message":"GraphQL API absent not found."}`
		eksNotFound     = `{"__type":"ResourceNotFoundException","message":"No cluster found for name: absent"}`
		mskNotFound     = `{"__type":"NotFoundException","message":"Cluster absent not found."}`
		efsNotFound     = `{"__type":"FileSystemNotFound","message":"File system 'fs-absent' does not exist."}`
		lambdaNotFound  = `{"__type":"ResourceNotFoundException","message":"Code signing config not found: absent"}`
	)
	// ec2NotFound is the Query-XML shape, whose HTTP 400 makes the status alone
	// useless: only the Code separates it from a genuine refusal.
	ec2NotFound := func(code string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><Response><Errors><Error><Code>` + code +
			`</Code><Message>the resource does not exist</Message></Error></Errors></Response>`
	}

	cases := []absentCase{
		// EC2 — HTTP 400, absence carried only in the Query-XML Code.
		// #1847: these three answered a generic InvalidId.NotFound until EC2's
		// store started reporting a miss with the resource's own AWS code. The
		// classifier reduces every one of them to "notfound", so teardown is
		// unchanged — which is the point of recording them: the codes moved
		// and nothing here had to.
		{"AWS::EC2::VPC", "vpc-absent", gone(400, ec2NotFound("InvalidVpcID.NotFound"))},
		{"AWS::EC2::Subnet", "subnet-absent", gone(400, ec2NotFound("InvalidSubnetID.NotFound"))},
		{"AWS::EC2::SecurityGroup", "sg-absent", gone(400, ec2NotFound("InvalidGroup.NotFound"))},
		{"AWS::EC2::InternetGateway", "igw-absent", gone(400, ec2NotFound("InvalidInternetGatewayID.NotFound"))},
		{"AWS::EC2::VPNGateway", "vgw-absent", gone(400, ec2NotFound("InvalidVpnGatewayID.NotFound"))},
		{"AWS::EC2::VPCGatewayAttachment", "vpc-absent|igw-absent", gone(400, ec2NotFound("InvalidInternetGatewayID.NotFound"))},
		{"AWS::EC2::VPCGatewayAttachment", "vpc-absent|vpn|vgw-absent", gone(400, ec2NotFound("InvalidVpnGatewayID.NotFound"))},
		{"AWS::EC2::RouteTable", "rtb-absent", gone(400, ec2NotFound("InvalidRouteTableID.NotFound"))},
		{"AWS::EC2::Route", "rtb-absent|0.0.0.0/0", gone(400, ec2NotFound("InvalidRouteTableID.NotFound"))},
		{"AWS::EC2::SubnetRouteTableAssociation", "rtbassoc-absent", gone(400, ec2NotFound("InvalidAssociationID.NotFound"))},
		{"AWS::EC2::EIP", "eipalloc-absent", gone(400, ec2NotFound("InvalidAllocationID.NotFound"))},
		{"AWS::EC2::NatGateway", "nat-absent", gone(400, ec2NotFound("NatGatewayNotFound"))},

		// API Gateway — HTTP 404 under NotFoundException.
		{"AWS::ApiGateway::RestApi", "absentapi", gone(404, apigwNotFound)},
		{"AWS::ApiGateway::Resource", "absentapi/absentres", gone(404, apigwNotFound)},
		{"AWS::ApiGateway::Method", "absentapi/absentres/GET", gone(404, apigwNotFound)},
		{"AWS::ApiGateway::Stage", "absentapi/absentstage", gone(404, apigwNotFound)},
		{"AWS::ApiGateway::ApiKey", "absentkey", gone(404, apigwNotFound)},
		{"AWS::ApiGateway::UsagePlan", "absentplan", gone(404, apigwNotFound)},
		{"AWS::ApiGateway::UsagePlanKey", "absentplan/absentkey", gone(404, apigwNotFound)},
		{"AWS::ApiGatewayV2::Api", "absentapi", gone(404, apigwNotFound)},
		{"AWS::ApiGatewayV2::Stage", "absentapi/absentstage", gone(404, apigwNotFound)},
		{"AWS::ApiGatewayV2::Integration", "absentapi/absentint", gone(404, apigwNotFound)},
		{"AWS::ApiGatewayV2::Route", "absentapi/absentroute", gone(404, apigwNotFound)},

		// AppRegistry.
		{"AWS::ServiceCatalogAppRegistry::Application", "absentapp",
			gone(404, `{"__type":"ResourceNotFoundException","message":"The resource absentapp was not found."}`)},
		{"AWS::ServiceCatalogAppRegistry::ResourceAssociation", "absentapp/CFN_STACK/absent-stack",
			gone(404, `{"__type":"ResourceNotFoundException","message":"The resource absentapp was not found."}`)},

		// AppSync.
		{"AWS::AppSync::Api", "absentapi", gone(404, `{"__type":"NotFoundException","message":"Api absentapi not found."}`)},
		{"AWS::AppSync::ChannelNamespace", "absentapi/absentns",
			gone(404, `{"__type":"NotFoundException","message":"Channel namespace absentns not found."}`)},
		{"AWS::AppSync::GraphQLApi", "absentapi", gone(404, appsyncNotFound)},
		{"AWS::AppSync::ApiKey", "absentapi/absentkey", gone(404, appsyncNotFound)},
		{"AWS::AppSync::FunctionConfiguration", "absentapi/absentfn", gone(404, appsyncNotFound)},
		{"AWS::AppSync::DataSource", "absentapi/absentds", gone(404, appsyncNotFound)},
		{"AWS::AppSync::Resolver", "absentapi/Query/absentfield", gone(404, appsyncNotFound)},
		{"AWS::AppSync::DomainName", "absent.example.com", gone(404, appsyncNotFound)},
		{"AWS::AppSync::DomainNameApiAssociation", "absent.example.com/absentapi", gone(404, appsyncNotFound)},
		{"AWS::AppSync::ApiCache", "absentapi", gone(404, appsyncNotFound)},
		{"AWS::AppSync::SourceApiAssociation", "absentmerged/absentassoc",
			gone(404, `{"__type":"NotFoundException","message":"Source API association absentassoc not found."}`)},

		// EFS — HTTP 404 under a per-resource code.
		{"AWS::EFS::FileSystem", "fs-absent", gone(404, efsNotFound)},
		{"AWS::EFS::MountTarget", "fsmt-absent",
			gone(404, `{"__type":"MountTargetNotFound","message":"Mount target 'fsmt-absent' does not exist."}`)},
		{"AWS::EFS::AccessPoint", "fsap-absent",
			gone(404, `{"__type":"AccessPointNotFound","message":"Access point 'fsap-absent' does not exist."}`)},

		// ECS. The cluster answers a code that names absence; the task
		// definition does not, which is why its handler carries its own check.
		{"AWS::ECS::Cluster", "absent-cluster",
			gone(400, `{"__type":"ClusterNotFoundException","message":"Cluster not found: absent-cluster"}`)},
		{"AWS::ECS::TaskDefinition", "absent-td:1",
			gone(400, `{"__type":"ClientException","message":"Unable to describe task definition: absent-td:1"}`)},

		// EKS.
		{"AWS::EKS::Cluster", "absent-cluster", gone(404, eksNotFound)},
		{"AWS::EKS::Nodegroup", "absent-cluster/absent-ng", gone(404, eksNotFound)},
		{"AWS::EKS::FargateProfile", "absent-cluster/absent-fp", gone(404, eksNotFound)},
		{"AWS::EKS::Addon", "absent-cluster/absent-addon", gone(404, eksNotFound)},
		{"AWS::EKS::AccessEntry", "absent-cluster/arn:aws:iam::000000000000:role/absent", gone(404, eksNotFound)},
		{"AWS::EKS::PodIdentityAssociation", "absent-assoc", gone(404, eksNotFound)},

		// MSK, Pipes, WAFv2.
		{"AWS::MSK::Cluster", "arn:aws:kafka:us-east-1:000000000000:cluster/absent/1111-2", gone(404, mskNotFound)},
		{"AWS::MSK::Configuration", "arn:aws:kafka:us-east-1:000000000000:configuration/absent/1111-2", gone(404, mskNotFound)},
		{"AWS::Pipes::Pipe", "absent-pipe", gone(404, `{"__type":"NotFoundException","message":"Pipe \"absent-pipe\" does not exist."}`)},
		{"AWS::WAFv2::WebACL", "REGIONAL/absent-id",
			gone(400, `{"__type":"WAFNonexistentItemException","message":"WebACL absent-id not found"}`)},

		// Route 53 — Query-XML absence on HTTP 404.
		{"AWS::Route53::HostedZone", "ABSENTZONE",
			gone(404, `<?xml version="1.0" encoding="UTF-8"?><ErrorResponse><Error><Type>Sender</Type>`+
				`<Code>NoSuchHostedZone</Code><Message>No hosted zone found with ID: ABSENTZONE</Message></Error></ErrorResponse>`)},
		{"AWS::Route53::HealthCheck", "absent-hc",
			gone(404, `<?xml version="1.0" encoding="UTF-8"?><ErrorResponse><Error><Type>Sender</Type>`+
				`<Code>NoSuchHealthCheck</Code><Message>No health check</Message></Error></ErrorResponse>`)},
		// The record set is fetched before it is deleted, so its absence has two
		// shapes: a zone that is gone, and a record that vanished between the
		// two calls. The second is what the delete's own allowance is for.
		{"AWS::Route53::RecordSet", "ABSENTZONE/www.example.com/A", route53RecordVanishedAnswer},

		// EventBridge, KMS, Step Functions. Several of these answer HTTP 200 for
		// an absent resource, which is why the rule is stated as "the resource
		// is gone" rather than "the delete succeeded".
		{"AWS::Events::EventBus", "arn:aws:events:us-east-1:000000000000:event-bus/absent-bus",
			gone(400, `{"__type":"ResourceNotFoundException","message":"Event bus absent-bus does not exist."}`)},
		{"AWS::Events::Rule", "arn:aws:events:us-east-1:000000000000:rule/absent-rule",
			gone(400, `{"__type":"ResourceNotFoundException","message":"Rule absent-rule does not exist."}`)},
		{"AWS::KMS::Key", "absent-key", gone(400, `{"__type":"NotFoundException","message":"Invalid keyId absent-key"}`)},
		{"AWS::KMS::Alias", "alias/absent", gone(400, `{"__type":"NotFoundException","message":"Alias does not exist"}`)},
		{"AWS::StepFunctions::StateMachine", "arn:aws:states:us-east-1:000000000000:stateMachine:absent",
			gone(400, `{"__type":"StateMachineDoesNotExist","message":"State machine does not exist"}`)},
		{"AWS::StepFunctions::StateMachineAlias", "arn:aws:states:us-east-1:000000000000:stateMachine:absent:PROD",
			gone(400, `{"__type":"ResourceNotFound","message":"Resource not found: 'arn:aws:states:us-east-1:000000000000:stateMachine:absent:PROD'"}`)},

		// Lambda and S3.
		{"AWS::Lambda::Function", "absent-fn",
			gone(404, `{"__type":"ResourceNotFoundException","message":"Function not found: absent-fn"}`)},
		{"AWS::Lambda::Permission", "absent-fn|absent-sid",
			gone(404, `{"__type":"ResourceNotFoundException","message":"The resource you requested does not exist."}`)},
		{"AWS::Lambda::EventSourceMapping", "absent-uuid",
			gone(404, `{"__type":"ResourceNotFoundException","message":"The event source arn (absent-uuid) is incorrect"}`)},
		{"AWS::Lambda::CodeSigningConfig", "absent-csc", gone(404, lambdaNotFound)},
		{"AWS::S3::BucketPolicy", "absent-bucket",
			gone(404, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchBucket</Code>`+
				`<Message>The specified bucket does not exist: absent-bucket</Message></Error>`)},

		// CloudFront reads the distribution before it deletes it, and answers
		// the same way for both calls.
		{"AWS::CloudFront::Distribution", "ABSENTDIST",
			gone(404, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchDistribution</Code>`+
				`<Message>The specified distribution does not exist: ABSENTDIST</Message></Error>`)},

		// RDS. Deletion protection is the one refusal this handler reports, and
		// an absent cluster is not it.
		{"AWS::RDS::DBCluster", "absent-cluster",
			gone(400, `<?xml version="1.0" encoding="UTF-8"?><ErrorResponse><Error><Type>Sender</Type>`+
				`<Code>DBClusterNotFoundFault</Code><Message>DBCluster absent-cluster not found.</Message></Error></ErrorResponse>`)},
	}

	for _, tc := range cases {
		t.Run(tc.resType+" "+tc.physicalID, func(t *testing.T) {
			handler, ok := resourceHandlers[tc.resType]
			if !ok {
				t.Fatalf("no handler registered for %s", tc.resType)
			}
			p := newProvisionerTestFixture(t)
			router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				code, body := tc.answer(r)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				w.Write([]byte(body)) //nolint:errcheck
			})
			p.initRouter(router)

			err := p.invokeDelete(context.Background(), handler, router, tc.physicalID, nil,
				&resolveContext{Region: "us-east-1", StackName: "absent-stack"})
			if err != nil {
				t.Fatalf("Delete of an absent %s reported %v, want nil — a resource that is "+
					"already gone is a successful teardown and must not fail a rollback",
					tc.resType, err)
			}
		})
	}
}

// route53RecordVanishedAnswer lets the zone listing answer with the record and
// then reports the record absent on the change that deletes it, which is the
// only way to reach the delete's own allowance: the handler fetches first, and
// a zone that is already gone stops it before the delete is ever dispatched.
func route53RecordVanishedAnswer(r *http.Request) (int, string) {
	if r.Method == http.MethodGet {
		return 200, `<?xml version="1.0" encoding="UTF-8"?><ListResourceRecordSetsResponse>` +
			`<ResourceRecordSets><ResourceRecordSet><Name>www.example.com.</Name><Type>A</Type>` +
			`<TTL>300</TTL><ResourceRecords><ResourceRecord><Value>1.2.3.4</Value></ResourceRecord>` +
			`</ResourceRecords></ResourceRecordSet></ResourceRecordSets></ListResourceRecordSetsResponse>`
	}
	return 400, `<?xml version="1.0" encoding="UTF-8"?><ErrorResponse><Error><Type>Sender</Type>` +
		`<Code>InvalidChangeBatch</Code><Message>Tried to delete resource record set ` +
		`[name='www.example.com.', type='A'] but it was not found</Message></Error></ErrorResponse>`
}

// The allowance may not be so wide that it hides a resource which is still
// standing. A service that fails, or refuses, is a resource the teardown did
// not remove, and every handler has to say so.
func TestResourceDelete_aServiceFailureIsStillReported(t *testing.T) {
	cases := []struct {
		resType    string
		physicalID string
		code       int
		body       string
		// wantCode and wantMessage are the two things the service said that the
		// teardown failure has to reach the operator carrying — see
		// status_reason.go, which renders them into CloudFormation's shape
		// rather than pasting the wire body in.
		wantCode    string
		wantMessage string
	}{
		// EC2's refusal to delete a resource something else still depends on.
		// It must never be read as absence: the resource is very much there.
		{"AWS::EC2::Subnet", "subnet-1234", 400,
			`<?xml version="1.0" encoding="UTF-8"?><Response><Errors><Error><Code>DependencyViolation</Code>` +
				`<Message>The subnet 'subnet-1234' has dependencies and cannot be deleted.</Message></Error></Errors></Response>`,
			"DependencyViolation", "The subnet 'subnet-1234' has dependencies and cannot be deleted."},
		{"AWS::EC2::SecurityGroup", "sg-1234", 400,
			`<?xml version="1.0" encoding="UTF-8"?><Response><Errors><Error><Code>DependencyViolation</Code>` +
				`<Message>resource sg-1234 has a dependent object</Message></Error></Errors></Response>`,
			"DependencyViolation", "resource sg-1234 has a dependent object"},
		{"AWS::EKS::Cluster", "live-cluster", 500,
			`{"__type":"InternalFailure","message":"cluster delete failed"}`,
			"InternalFailure", "cluster delete failed"},
		{"AWS::ApiGateway::RestApi", "liveapi", 500,
			`{"__type":"InternalFailure","message":"rest api delete failed"}`,
			"InternalFailure", "rest api delete failed"},
		{"AWS::Events::Rule", "arn:aws:events:us-east-1:000000000000:rule/live-rule", 400,
			`{"__type":"ValidationException","message":"Rule can't be deleted since it has targets."}`,
			"ValidationException", "Rule can't be deleted since it has targets."},
		{"AWS::KMS::Key", "live-key", 400,
			`{"__type":"KMSInvalidStateException","message":"Key is pending deletion"}`,
			"KMSInvalidStateException", "Key is pending deletion"},
		{"AWS::ECS::TaskDefinition", "live-td:1", 400,
			`{"__type":"ClientException","message":"taskDefinition must include a revision (family:revision)."}`,
			"ClientException", "taskDefinition must include a revision (family:revision)."},
	}

	for _, tc := range cases {
		t.Run(tc.resType, func(t *testing.T) {
			handler, ok := resourceHandlers[tc.resType]
			if !ok {
				t.Fatalf("no handler registered for %s", tc.resType)
			}
			p := newProvisionerTestFixture(t)
			router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body)) //nolint:errcheck
			})
			p.initRouter(router)

			err := p.invokeDelete(context.Background(), handler, router, tc.physicalID, nil,
				&resolveContext{Region: "us-east-1", StackName: "live-stack"})
			if err == nil {
				t.Fatalf("Delete of %s reported success over a resource that is still standing", tc.resType)
			}
			for _, want := range []string{
				tc.wantCode, tc.wantMessage, "Status Code: " + strconv.Itoa(tc.code),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not carry %q — the operator has to be told what the service said", err, want)
				}
			}
		})
	}
}
