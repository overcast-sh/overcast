package cloudformation_test

// generated_names_test.go — resources whose name the template does not supply.
//
// Almost every AWS::*::* name property is "Required: No", and CloudFormation
// mints "{StackName}-{LogicalID}-{RANDOM}" when the template leaves it out.
// CDK relies on this everywhere: its L2 constructs deliberately do not name
// resources unless you pass an explicit name, because a physical name makes
// the resource un-replaceable. So the no-name path is the *common* path for a
// CDK stack, not an edge case — a handler that forwards the empty string, or
// drops the name parameter and lets the service decide, breaks a stack that
// real CloudFormation deploys.
//
// This table is the systematic version of the AWS::CloudWatch::Alarm bug: one
// minimal, name-less resource per type CDK emits without a name.
//
// The generated name also has to satisfy the naming rule of the service that
// will hold it — 32 characters for an ELBv2 load balancer, lowercase for a
// Glue database, 40 for an ElastiCache replication group — so a row may carry
// the documented length and character set alongside its template.

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// Resources a row may need before the resource under test can be created: a
// load balancer for a listener, a cluster for a nodegroup. Each is named
// explicitly — it is the resource under test whose name has to be generated,
// and an explicitly named dependency keeps the row's assertion about one
// resource.
const (
	depLoadBalancer = `"DepLoadBalancer": {"Type": "AWS::ElasticLoadBalancingV2::LoadBalancer",
		"Properties": {"Name": "dep-lb", "Subnets": ["subnet-1"]}},`
	depEKSCluster = `"DepEksCluster": {"Type": "AWS::EKS::Cluster", "Properties": {
		"Name": "dep-cluster", "RoleArn": "arn:aws:iam::000000000000:role/eks",
		"ResourcesVpcConfig": {"SubnetIds": ["subnet-1"]}}},`
	depUserPool = `"DepUserPool": {"Type": "AWS::Cognito::UserPool",
		"Properties": {"UserPoolName": "dep-pool"}},`
	depLaunchConfig = `"DepLaunchConfig": {"Type": "AWS::AutoScaling::LaunchConfiguration", "Properties": {
		"LaunchConfigurationName": "dep-lc", "ImageId": "ami-123", "InstanceType": "t3.micro"}},`
	depLogGroup = `"DepLogGroup": {"Type": "AWS::Logs::LogGroup",
		"Properties": {"LogGroupName": "/dep/metric-filter"}},`
)

// nameConstraint is a service's documented rule for the name CloudFormation
// generates: how long it may be and which characters it may contain. A
// generated name that breaks one turns this bug from a duplicated name into a
// stack that cannot deploy at all, which is why #784 stopped short of
// converting these handlers without checking each rule.
type nameConstraint struct {
	// nameOf recovers the generated name from the physical ID. Nil means the
	// physical ID is the name.
	nameOf func(physID string) string
	maxLen int
	// charset is the documented character set, anchored, including any
	// leading/trailing-character rule.
	charset string
}

// check asserts the name recovered from physID satisfies the rule.
func (c nameConstraint) check(t *testing.T, physID string) {
	t.Helper()
	name := physID
	if c.nameOf != nil {
		name = c.nameOf(physID)
	}
	if name == "" {
		t.Fatalf("could not recover the generated name from physical ID %q", physID)
	}
	if c.maxLen > 0 && len(name) > c.maxLen {
		t.Errorf("generated name %q is %d characters, over the documented maximum of %d",
			name, len(name), c.maxLen)
	}
	if c.charset != "" && !regexp.MustCompile(c.charset).MatchString(name) {
		t.Errorf("generated name %q does not match the documented character set %s",
			name, c.charset)
	}
}

// afterLast returns the part of s after the last sep — the name at the end of
// an ARN such as arn:aws:firehose:…:deliverystream/<name>.
func afterLast(s, sep string) string {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[i+len(sep):]
	}
	return s
}

// nthSegment returns the i-th sep-separated segment of s — the name in the
// middle of an ARN such as arn:aws:kafka:…:cluster/<name>/<uuid>.
func nthSegment(s, sep string, i int) string {
	parts := strings.Split(s, sep)
	if i >= len(parts) {
		return ""
	}
	return parts[i]
}

// responseFieldValues returns every value the response gives for a named JSON
// or XML field. It is deliberately shallow: the question these tests ask of a
// list response is only "how many distinct names are in it", which does not
// justify a response struct per service.
func responseFieldValues(t *testing.T, resp *http.Response, field string) []string {
	t.Helper()
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := string(readBody(t, resp))

	var out []string
	for _, pattern := range []string{`"` + field + `"\s*:\s*"([^"]*)"`, `<` + field + `>([^<]*)</` + field + `>`} {
		for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// Documented character sets, shared by the rows that share a rule.
const (
	// ELBv2: "only alphanumeric characters or hyphens", "must not begin or
	// end with a hyphen".
	charsetELBv2 = `^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$`
	// RDS and ElastiCache identifiers: letters, digits and hyphens, stored
	// lowercase, first character a letter, no trailing hyphen.
	charsetLowerIdentifier = `^[a-z][a-z0-9-]*[a-z0-9]$`
	// Athena, Firehose and Pipes: letters, digits, period, hyphen,
	// underscore.
	charsetDotDashUnderscore = `^[a-zA-Z0-9._-]+$`
	// Backup vault and SES template names: letters, digits, hyphen,
	// underscore — no period.
	charsetDashUnderscore = `^[0-9A-Za-z_-]+$`
	// EKS cluster: ^[0-9A-Za-z][A-Za-z0-9\-_]*, and CloudFormation forbids
	// the underscore the pattern admits.
	charsetEKSCluster = `^[0-9A-Za-z][A-Za-z0-9-]*$`
	// CloudTrail: must start and end with a letter or a digit.
	charsetCloudTrail = `^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`
	// S3 bucket: lowercase letters, digits, periods and hyphens, beginning
	// and ending with a letter or a digit.
	charsetS3Bucket = `^[a-z0-9][a-z0-9.-]*[a-z0-9]$`
	// MSK configuration: ^[0-9A-Za-z][0-9A-Za-z-]{0,}$.
	charsetMSKConfiguration = `^[0-9A-Za-z][0-9A-Za-z-]*$`
	// Glue folds a database name to lowercase when it stores it, and RDS and
	// ElastiCache store their group names lowercase, so what matters for
	// those is that nothing uppercase was generated to be folded away.
	charsetNoUppercase = `^[^A-Z]+$`
)

func TestCreateStack_resourcesWithoutNames_areNamedByCloudFormation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		logicalID  string
		properties string
		// deps are extra resources the row needs, as raw template entries
		// ending in a comma.
		deps string
		// constraint, when set, is checked against the generated name.
		constraint *nameConstraint
	}{
		{
			name:      "AWS::SQS::Queue",
			logicalID: "Queue",
			properties: `{
        "Type": "AWS::SQS::Queue",
        "Properties": {}
      }`,
		},
		{
			name:      "AWS::SNS::Topic",
			logicalID: "Topic",
			properties: `{
        "Type": "AWS::SNS::Topic",
        "Properties": {}
      }`,
		},
		{
			name:      "AWS::DynamoDB::Table",
			logicalID: "Table",
			properties: `{
        "Type": "AWS::DynamoDB::Table",
        "Properties": {
          "KeySchema": [{"AttributeName": "pk", "KeyType": "HASH"}],
          "AttributeDefinitions": [{"AttributeName": "pk", "AttributeType": "S"}],
          "BillingMode": "PAY_PER_REQUEST"
        }
      }`,
		},
		{
			name:      "AWS::Logs::LogGroup",
			logicalID: "Logs",
			properties: `{
        "Type": "AWS::Logs::LogGroup",
        "Properties": {"RetentionInDays": 7}
      }`,
		},
		{
			name:      "AWS::Logs::MetricFilter",
			logicalID: "Filter",
			deps:      depLogGroup,
			properties: `{
        "Type": "AWS::Logs::MetricFilter",
        "Properties": {
          "LogGroupName": {"Ref": "DepLogGroup"},
          "FilterPattern": "ERROR",
          "MetricTransformations": [{"MetricName": "Errors", "MetricNamespace": "App", "MetricValue": "1"}]
        }
      }`,
			// FilterName: 1–512 characters, no ':' or '*'.
			constraint: &nameConstraint{maxLen: 512, charset: `^[^:*]+$`},
		},
		{
			name:      "AWS::IAM::Role",
			logicalID: "Role",
			properties: `{
        "Type": "AWS::IAM::Role",
        "Properties": {
          "AssumeRolePolicyDocument": {
            "Version": "2012-10-17",
            "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}]
          }
        }
      }`,
		},
		{
			name:      "AWS::Events::Rule",
			logicalID: "Rule",
			properties: `{
        "Type": "AWS::Events::Rule",
        "Properties": {
          "EventPattern": {"source": ["com.example"]},
          "State": "ENABLED"
        }
      }`,
		},
		{
			name:      "AWS::StepFunctions::StateMachine",
			logicalID: "Machine",
			properties: `{
        "Type": "AWS::StepFunctions::StateMachine",
        "Properties": {
          "RoleArn": "arn:aws:iam::000000000000:role/sfn",
          "DefinitionString": "{\"StartAt\":\"Done\",\"States\":{\"Done\":{\"Type\":\"Succeed\"}}}"
        }
      }`,
		},
		{
			name:      "AWS::Scheduler::ScheduleGroup",
			logicalID: "Group",
			properties: `{
        "Type": "AWS::Scheduler::ScheduleGroup",
        "Properties": {}
      }`,
		},
		{
			name:      "AWS::Scheduler::Schedule",
			logicalID: "Schedule",
			properties: `{
        "Type": "AWS::Scheduler::Schedule",
        "Properties": {
          "ScheduleExpression": "rate(5 minutes)",
          "FlexibleTimeWindow": {"Mode": "OFF"},
          "Target": {
            "Arn": "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
            "RoleArn": "arn:aws:iam::000000000000:role/scheduler-role"
          }
        }
      }`,
		},
		{
			name:      "AWS::SecretsManager::Secret",
			logicalID: "Secret",
			properties: `{
        "Type": "AWS::SecretsManager::Secret",
        "Properties": {"SecretString": "shh"}
      }`,
		},
		{
			name:      "AWS::Kinesis::Stream",
			logicalID: "Stream",
			properties: `{
        "Type": "AWS::Kinesis::Stream",
        "Properties": {"ShardCount": 1}
      }`,
		},
		{
			name:      "AWS::ApiGateway::RestApi",
			logicalID: "Api",
			properties: `{
        "Type": "AWS::ApiGateway::RestApi",
        "Properties": {}
      }`,
		},
		{
			name:      "AWS::ECS::Cluster",
			logicalID: "Cluster",
			properties: `{
        "Type": "AWS::ECS::Cluster",
        "Properties": {}
      }`,
		},

		// #788 — the handlers that named an unnamed resource from the stack
		// name alone. One row per converted type.
		{
			name:       "AWS::ServiceCatalogAppRegistry::Application",
			logicalID:  "App",
			properties: `{"Type": "AWS::ServiceCatalogAppRegistry::Application", "Properties": {}}`,
		},
		{
			name:       "AWS::EC2::SecurityGroup",
			logicalID:  "SecurityGroup",
			properties: `{"Type": "AWS::EC2::SecurityGroup", "Properties": {"GroupDescription": "test"}}`,
		},
		{
			name:       "AWS::CloudTrail::Trail",
			logicalID:  "Trail",
			properties: `{"Type": "AWS::CloudTrail::Trail", "Properties": {}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return afterLast(id, "/") },
				maxLen: 128, charset: charsetCloudTrail,
			},
		},
		{
			name:       "AWS::Backup::BackupVault",
			logicalID:  "Vault",
			properties: `{"Type": "AWS::Backup::BackupVault", "Properties": {}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return afterLast(id, ":") },
				maxLen: 50, charset: charsetDashUnderscore,
			},
		},
		{
			name:      "AWS::Shield::Protection",
			logicalID: "Protection",
			properties: `{"Type": "AWS::Shield::Protection", "Properties": {
        "ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/x/1"}}`,
		},
		{
			name:       "AWS::KinesisFirehose::DeliveryStream",
			logicalID:  "Firehose",
			properties: `{"Type": "AWS::KinesisFirehose::DeliveryStream", "Properties": {}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return afterLast(id, "/") },
				maxLen: 64, charset: charsetDotDashUnderscore,
			},
		},
		{
			name:       "AWS::Athena::WorkGroup",
			logicalID:  "WorkGroup",
			properties: `{"Type": "AWS::Athena::WorkGroup", "Properties": {}}`,
			constraint: &nameConstraint{maxLen: 128, charset: charsetDotDashUnderscore},
		},
		{
			name:       "AWS::Glue::Database",
			logicalID:  "GlueDatabase",
			properties: `{"Type": "AWS::Glue::Database", "Properties": {"DatabaseInput": {}}}`,
			constraint: &nameConstraint{maxLen: 255, charset: charsetNoUppercase},
		},
		{
			name:      "AWS::RDS::DBCluster",
			logicalID: "DBCluster",
			properties: `{"Type": "AWS::RDS::DBCluster", "Properties": {
        "Engine": "aurora-mysql", "MasterUsername": "admin", "MasterUserPassword": "correct-horse"}}`,
			constraint: &nameConstraint{maxLen: 63, charset: charsetLowerIdentifier},
		},
		{
			name:      "AWS::RDS::DBSubnetGroup",
			logicalID: "DBSubnetGroup",
			properties: `{"Type": "AWS::RDS::DBSubnetGroup", "Properties": {
        "DBSubnetGroupDescription": "test", "SubnetIds": ["subnet-1", "subnet-2"]}}`,
			constraint: &nameConstraint{maxLen: 255, charset: charsetNoUppercase},
		},
		{
			name:      "AWS::RDS::DBParameterGroup",
			logicalID: "DBParameterGroup",
			properties: `{"Type": "AWS::RDS::DBParameterGroup", "Properties": {
        "Family": "mysql8.0", "Description": "test"}}`,
			constraint: &nameConstraint{maxLen: 255, charset: charsetLowerIdentifier},
		},
		{
			name:       "AWS::Cognito::UserPool",
			logicalID:  "UserPool",
			properties: `{"Type": "AWS::Cognito::UserPool", "Properties": {}}`,
		},
		{
			name:      "AWS::Cognito::UserPoolClient",
			logicalID: "UserPoolClient",
			deps:      depUserPool,
			properties: `{"Type": "AWS::Cognito::UserPoolClient", "Properties": {
        "UserPoolId": {"Ref": "DepUserPool"}}}`,
		},
		{
			name:      "AWS::SES::Template",
			logicalID: "EmailTemplate",
			properties: `{"Type": "AWS::SES::Template", "Properties": {
        "Template": {"SubjectPart": "hi", "TextPart": "hello"}}}`,
			constraint: &nameConstraint{maxLen: 64, charset: charsetDashUnderscore},
		},
		{
			name:      "AWS::ElastiCache::ReplicationGroup",
			logicalID: "ReplicationGroup",
			properties: `{"Type": "AWS::ElastiCache::ReplicationGroup", "Properties": {
        "ReplicationGroupDescription": "test", "CacheNodeType": "cache.t3.micro", "Engine": "redis"}}`,
			constraint: &nameConstraint{maxLen: 40, charset: charsetLowerIdentifier},
		},
		{
			name:      "AWS::ElastiCache::SubnetGroup",
			logicalID: "CacheSubnetGroup",
			properties: `{"Type": "AWS::ElastiCache::SubnetGroup", "Properties": {
        "CacheSubnetGroupDescription": "test", "SubnetIds": ["subnet-1"]}}`,
			constraint: &nameConstraint{maxLen: 255, charset: charsetNoUppercase},
		},
		{
			name:       "AWS::ElastiCache::ServerlessCache",
			logicalID:  "ServerlessCache",
			properties: `{"Type": "AWS::ElastiCache::ServerlessCache", "Properties": {"Engine": "redis"}}`,
			constraint: &nameConstraint{maxLen: 40, charset: charsetNoUppercase},
		},
		{
			name:      "AWS::ElasticLoadBalancingV2::LoadBalancer",
			logicalID: "LoadBalancer",
			properties: `{"Type": "AWS::ElasticLoadBalancingV2::LoadBalancer", "Properties": {
        "Subnets": ["subnet-1"]}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return nthSegment(id, "/", 2) },
				maxLen: 32, charset: charsetELBv2,
			},
		},
		{
			name:       "AWS::ElasticLoadBalancingV2::TargetGroup",
			logicalID:  "TargetGroup",
			properties: `{"Type": "AWS::ElasticLoadBalancingV2::TargetGroup", "Properties": {"Port": 80, "Protocol": "HTTP"}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return nthSegment(id, "/", 1) },
				maxLen: 32, charset: charsetELBv2,
			},
		},
		{
			name:      "AWS::ElasticLoadBalancingV2::Listener",
			logicalID: "Listener",
			deps:      depLoadBalancer,
			properties: `{"Type": "AWS::ElasticLoadBalancingV2::Listener", "Properties": {
        "LoadBalancerArn": {"Ref": "DepLoadBalancer"}, "Port": 80, "Protocol": "HTTP",
        "DefaultActions": [{"Type": "fixed-response",
          "FixedResponseConfig": {"StatusCode": "200"}}]}}`,
		},
		{
			name:      "AWS::AutoScaling::AutoScalingGroup",
			logicalID: "AutoScalingGroup",
			deps:      depLaunchConfig,
			// AvailabilityZones only because a group has to name a zone or a
			// subnet at all (#1843); this case is about the generated name.
			properties: `{"Type": "AWS::AutoScaling::AutoScalingGroup", "Properties": {
        "MinSize": "0", "MaxSize": "1", "AvailabilityZones": ["us-east-1a"],
        "LaunchConfigurationName": {"Ref": "DepLaunchConfig"}}}`,
			// No charset: AutoScalingGroupName admits every printable ASCII
			// character except a colon.
			constraint: &nameConstraint{maxLen: 255},
		},
		{
			name:      "AWS::AutoScaling::LaunchConfiguration",
			logicalID: "LaunchConfiguration",
			properties: `{"Type": "AWS::AutoScaling::LaunchConfiguration", "Properties": {
        "ImageId": "ami-123", "InstanceType": "t3.micro"}}`,
			// CreateLaunchConfiguration documents a length and nothing else.
			constraint: &nameConstraint{maxLen: 255},
		},
		{
			name:      "AWS::EKS::Cluster",
			logicalID: "EksCluster",
			properties: `{"Type": "AWS::EKS::Cluster", "Properties": {
        "RoleArn": "arn:aws:iam::000000000000:role/eks",
        "ResourcesVpcConfig": {"SubnetIds": ["subnet-1"]}}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return afterLast(id, "/") },
				maxLen: 100, charset: charsetEKSCluster,
			},
		},
		{
			name:      "AWS::EKS::Nodegroup",
			logicalID: "Nodegroup",
			deps:      depEKSCluster,
			properties: `{"Type": "AWS::EKS::Nodegroup", "DependsOn": "DepEksCluster", "Properties": {
        "ClusterName": "dep-cluster",
        "NodeRole": "arn:aws:iam::000000000000:role/ng", "Subnets": ["subnet-1"]}}`,
		},
		{
			name:      "AWS::EKS::FargateProfile",
			logicalID: "FargateProfile",
			deps:      depEKSCluster,
			properties: `{"Type": "AWS::EKS::FargateProfile", "DependsOn": "DepEksCluster", "Properties": {
        "ClusterName": "dep-cluster",
        "PodExecutionRoleArn": "arn:aws:iam::000000000000:role/fp"}}`,
		},
		// AWS::EKS::Addon is deliberately absent from this table: unlike the
		// resources above, AddonName is Required: Yes and CloudFormation
		// never mints one when a template omits it — see
		// eks_addon_name_test.go in tests/integration/cloudformation.
		{
			name:      "AWS::MSK::Cluster",
			logicalID: "MskCluster",
			properties: `{"Type": "AWS::MSK::Cluster", "Properties": {
        "KafkaVersion": "2.8.1", "NumberOfBrokerNodes": 1,
        "BrokerNodeGroupInfo": {"InstanceType": "kafka.m5.large", "ClientSubnets": ["subnet-1"]}}}`,
			// MSK documents a 64-character cap for ClusterName and no pattern.
			constraint: &nameConstraint{
				nameOf: func(id string) string { return nthSegment(id, "/", 1) },
				maxLen: 64,
			},
		},
		{
			name:      "AWS::MSK::Configuration",
			logicalID: "MskConfiguration",
			properties: `{"Type": "AWS::MSK::Configuration", "Properties": {
        "ServerProperties": "auto.create.topics.enable=true"}}`,
			constraint: &nameConstraint{
				nameOf:  func(id string) string { return nthSegment(id, "/", 1) },
				charset: charsetMSKConfiguration,
			},
		},
		{
			name:      "AWS::Pipes::Pipe",
			logicalID: "Pipe",
			properties: `{"Type": "AWS::Pipes::Pipe", "Properties": {
        "Source": "arn:aws:sqs:us-east-1:000000000000:src",
        "Target": "arn:aws:sqs:us-east-1:000000000000:dst",
        "RoleArn": "arn:aws:iam::000000000000:role/pipes"}}`,
			constraint: &nameConstraint{
				nameOf: func(id string) string { return afterLast(id, "/") },
				maxLen: 64, charset: charsetDotDashUnderscore,
			},
		},
		{
			name:      "AWS::WAFv2::WebACL",
			logicalID: "WebACL",
			properties: `{"Type": "AWS::WAFv2::WebACL", "Properties": {
        "Scope": "REGIONAL", "DefaultAction": {"Allow": {}},
        "VisibilityConfig": {"CloudWatchMetricsEnabled": false, "MetricName": "m",
          "SampledRequestsEnabled": false}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			stackName := "noname-" + strings.ToLower(tc.logicalID)
			template := fmt.Sprintf(`{"Resources": {%s%q: %s}}`, tc.deps, tc.logicalID, tc.properties)

			create := cfnQuery(t, srv, "CreateStack", url.Values{
				"StackName":    {stackName},
				"TemplateBody": {template},
			})
			defer create.Body.Close()
			helpers.AssertStatus(t, create, http.StatusOK)

			status := waitForStackStatusIn(t, srv, stackName,
				"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
			if status != "CREATE_COMPLETE" {
				t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
					status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
			}

			// And: the resource has a physical name to be referred to by. An
			// empty ID means Ref on the resource resolves to nothing, and the
			// deferred delete at cleanup or rollback has nothing to delete.
			physID := describeStackResourceIDs(t, srv, stackName)[tc.logicalID]
			if physID == "" {
				t.Fatal("resource has no physical ID")
			}

			// And: the generated name fits what the service documents. A
			// handler that names an unnamed resource has to truncate to the
			// service's cap and stay inside its character set, or it trades a
			// duplicated name for a stack that cannot deploy.
			if tc.constraint != nil {
				tc.constraint.check(t, physID)
			}
		})
	}
}

// TestCreateStack_twoUnnamedResourcesOfOneType_doNotCollide pins the other
// half of CloudFormation's naming rule: the generated name carries the logical
// ID, so two unnamed resources of the same type in one stack get two names.
// A fallback built from the stack name alone gives them both the same name,
// and the second silently overwrites — or upserts onto — the first.
//
// How that fails depends on the type. Where the generated name *is* the
// physical ID the second create collides with the first, so the stack fails or
// two resources become one. Where the service mints its own ID the stack still
// completes and only the name is duplicated — a quieter fault, but real AWS
// rejects a duplicate security group name in a VPC and a duplicate web ACL
// name, so those rows check the names as well as the IDs.
func TestCreateStack_twoUnnamedResourcesOfOneType_doNotCollide(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource string
		// deps are extra resources the row needs, as raw template entries
		// ending in a comma. Both copies of the resource under test share them.
		deps string
		// names lists the resource names the service reports, for the types
		// whose physical ID is not the name and so cannot show a duplicate.
		names func(t *testing.T, srv *helpers.TestServer) []string
	}{
		{name: "AWS::SQS::Queue", resource: `{"Type": "AWS::SQS::Queue", "Properties": {}}`},
		{name: "AWS::SNS::Topic", resource: `{"Type": "AWS::SNS::Topic", "Properties": {}}`},
		{name: "AWS::Logs::LogGroup", resource: `{"Type": "AWS::Logs::LogGroup", "Properties": {}}`},
		{name: "AWS::Logs::MetricFilter", deps: depLogGroup, resource: `{"Type": "AWS::Logs::MetricFilter", "Properties": {
			"LogGroupName": {"Ref": "DepLogGroup"}, "FilterPattern": "ERROR",
			"MetricTransformations": [{"MetricName": "Errors", "MetricNamespace": "App", "MetricValue": "1"}]}}`},
		{name: "AWS::Events::Rule", resource: `{"Type": "AWS::Events::Rule", "Properties": {"EventPattern": {"source": ["com.example"]}}}`},
		{name: "AWS::Scheduler::ScheduleGroup", resource: `{"Type": "AWS::Scheduler::ScheduleGroup", "Properties": {}}`},
		{name: "AWS::Scheduler::Schedule", resource: `{"Type": "AWS::Scheduler::Schedule", "Properties": {
			"ScheduleExpression": "rate(5 minutes)",
			"FlexibleTimeWindow": {"Mode": "OFF"},
			"Target": {
				"Arn": "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
				"RoleArn": "arn:aws:iam::000000000000:role/scheduler-role"}}}`},
		{name: "AWS::CloudWatch::Alarm", resource: `{"Type": "AWS::CloudWatch::Alarm", "Properties": {
			"MetricName": "Errors", "Namespace": "AWS/Lambda", "Statistic": "Sum",
			"Period": 60, "EvaluationPeriods": 1, "Threshold": 1,
			"ComparisonOperator": "GreaterThanThreshold"}}`},
		{name: "AWS::ECR::Repository", resource: `{"Type": "AWS::ECR::Repository", "Properties": {}}`},
		{name: "AWS::IAM::User", resource: `{"Type": "AWS::IAM::User", "Properties": {}}`},

		// #788 — the handlers that named an unnamed resource from the stack
		// name alone. One row per converted type.
		{
			name:     "AWS::EC2::SecurityGroup",
			resource: `{"Type": "AWS::EC2::SecurityGroup", "Properties": {"GroupDescription": "test"}}`,
			// Ref is the sg- ID, so the physical IDs differ either way; it is
			// the group name AWS requires to be unique within a VPC.
			names: func(t *testing.T, srv *helpers.TestServer) []string {
				return responseFieldValues(t, ec2Query(t, srv, "DescribeSecurityGroups", nil), "groupName")
			},
		},
		{
			name:     "AWS::ServiceCatalogAppRegistry::Application",
			resource: `{"Type": "AWS::ServiceCatalogAppRegistry::Application", "Properties": {}}`,
		},
		{
			name:     "AWS::CloudTrail::Trail",
			resource: `{"Type": "AWS::CloudTrail::Trail", "Properties": {}}`,
		},
		{
			name:     "AWS::Backup::BackupVault",
			resource: `{"Type": "AWS::Backup::BackupVault", "Properties": {}}`,
		},
		{
			name: "AWS::Shield::Protection",
			resource: `{"Type": "AWS::Shield::Protection", "Properties": {
				"ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/x/1"}}`,
			names: func(t *testing.T, srv *helpers.TestServer) []string {
				return responseFieldValues(t,
					awsJSONCall(t, srv, "AWSShield_20160616.", "ListProtections", "application/x-amz-json-1.1", map[string]any{}),
					"Name")
			},
		},
		{
			name:     "AWS::KinesisFirehose::DeliveryStream",
			resource: `{"Type": "AWS::KinesisFirehose::DeliveryStream", "Properties": {}}`,
		},
		{
			name:     "AWS::Athena::WorkGroup",
			resource: `{"Type": "AWS::Athena::WorkGroup", "Properties": {}}`,
		},
		{
			name:     "AWS::Glue::Database",
			resource: `{"Type": "AWS::Glue::Database", "Properties": {"DatabaseInput": {}}}`,
		},
		{
			name: "AWS::RDS::DBCluster",
			resource: `{"Type": "AWS::RDS::DBCluster", "Properties": {
				"Engine": "aurora-mysql", "MasterUsername": "admin", "MasterUserPassword": "correct-horse"}}`,
		},
		{
			name: "AWS::RDS::DBSubnetGroup",
			resource: `{"Type": "AWS::RDS::DBSubnetGroup", "Properties": {
				"DBSubnetGroupDescription": "test", "SubnetIds": ["subnet-1", "subnet-2"]}}`,
		},
		{
			name: "AWS::RDS::DBParameterGroup",
			resource: `{"Type": "AWS::RDS::DBParameterGroup", "Properties": {
				"Family": "mysql8.0", "Description": "test"}}`,
		},
		{
			name:     "AWS::Cognito::UserPool",
			resource: `{"Type": "AWS::Cognito::UserPool", "Properties": {}}`,
			names: func(t *testing.T, srv *helpers.TestServer) []string {
				return responseFieldValues(t,
					cognitoJSONCall(t, srv, "ListUserPools", map[string]any{"MaxResults": 60}), "Name")
			},
		},
		{
			name: "AWS::Cognito::UserPoolClient",
			deps: depUserPool,
			resource: `{"Type": "AWS::Cognito::UserPoolClient", "Properties": {
				"UserPoolId": {"Ref": "DepUserPool"}}}`,
		},
		{
			name: "AWS::SES::Template",
			resource: `{"Type": "AWS::SES::Template", "Properties": {
				"Template": {"SubjectPart": "hi", "TextPart": "hello"}}}`,
		},
		{
			name: "AWS::ElastiCache::ReplicationGroup",
			resource: `{"Type": "AWS::ElastiCache::ReplicationGroup", "Properties": {
				"ReplicationGroupDescription": "test", "CacheNodeType": "cache.t3.micro", "Engine": "redis"}}`,
		},
		{
			name: "AWS::ElastiCache::SubnetGroup",
			resource: `{"Type": "AWS::ElastiCache::SubnetGroup", "Properties": {
				"CacheSubnetGroupDescription": "test", "SubnetIds": ["subnet-1"]}}`,
		},
		{
			name:     "AWS::ElastiCache::ServerlessCache",
			resource: `{"Type": "AWS::ElastiCache::ServerlessCache", "Properties": {"Engine": "redis"}}`,
		},
		{
			name: "AWS::ElasticLoadBalancingV2::LoadBalancer",
			resource: `{"Type": "AWS::ElasticLoadBalancingV2::LoadBalancer", "Properties": {
				"Subnets": ["subnet-1"]}}`,
		},
		{
			name:     "AWS::ElasticLoadBalancingV2::TargetGroup",
			resource: `{"Type": "AWS::ElasticLoadBalancingV2::TargetGroup", "Properties": {"Port": 80, "Protocol": "HTTP"}}`,
		},
		{
			// An HTTP listener and an HTTPS listener on one load balancer is
			// the shape a CDK stack reaches for by default.
			name: "AWS::ElasticLoadBalancingV2::Listener",
			deps: depLoadBalancer,
			resource: `{"Type": "AWS::ElasticLoadBalancingV2::Listener", "Properties": {
				"LoadBalancerArn": {"Ref": "DepLoadBalancer"}, "Port": 80, "Protocol": "HTTP",
				"DefaultActions": [{"Type": "fixed-response",
					"FixedResponseConfig": {"StatusCode": "200"}}]}}`,
		},
		{
			name: "AWS::AutoScaling::AutoScalingGroup",
			deps: depLaunchConfig,
			// AvailabilityZones only because a group has to name a zone or a
			// subnet at all (#1843); this case is about the generated name.
			resource: `{"Type": "AWS::AutoScaling::AutoScalingGroup", "Properties": {
				"MinSize": "0", "MaxSize": "1", "AvailabilityZones": ["us-east-1a"],
				"LaunchConfigurationName": {"Ref": "DepLaunchConfig"}}}`,
		},
		{
			name: "AWS::AutoScaling::LaunchConfiguration",
			resource: `{"Type": "AWS::AutoScaling::LaunchConfiguration", "Properties": {
				"ImageId": "ami-123", "InstanceType": "t3.micro"}}`,
		},
		{
			name: "AWS::EKS::Cluster",
			resource: `{"Type": "AWS::EKS::Cluster", "Properties": {
				"RoleArn": "arn:aws:iam::000000000000:role/eks",
				"ResourcesVpcConfig": {"SubnetIds": ["subnet-1"]}}}`,
		},
		{
			// One nodegroup per instance type on one cluster is the common
			// shape, and neither is named.
			name: "AWS::EKS::Nodegroup",
			deps: depEKSCluster,
			resource: `{"Type": "AWS::EKS::Nodegroup", "DependsOn": "DepEksCluster", "Properties": {
				"ClusterName": "dep-cluster",
				"NodeRole": "arn:aws:iam::000000000000:role/ng", "Subnets": ["subnet-1"]}}`,
		},
		{
			name: "AWS::EKS::FargateProfile",
			deps: depEKSCluster,
			resource: `{"Type": "AWS::EKS::FargateProfile", "DependsOn": "DepEksCluster", "Properties": {
				"ClusterName": "dep-cluster",
				"PodExecutionRoleArn": "arn:aws:iam::000000000000:role/fp"}}`,
		},
		// AWS::EKS::Addon is deliberately absent from this table — see the
		// same note in TestCreateStack_resourcesWithoutNames_areNamedByCloudFormation
		// above.
		{
			name: "AWS::MSK::Cluster",
			resource: `{"Type": "AWS::MSK::Cluster", "Properties": {
				"KafkaVersion": "2.8.1", "NumberOfBrokerNodes": 1,
				"BrokerNodeGroupInfo": {"InstanceType": "kafka.m5.large", "ClientSubnets": ["subnet-1"]}}}`,
		},
		{
			name: "AWS::MSK::Configuration",
			resource: `{"Type": "AWS::MSK::Configuration", "Properties": {
				"ServerProperties": "auto.create.topics.enable=true"}}`,
		},
		{
			name: "AWS::Pipes::Pipe",
			resource: `{"Type": "AWS::Pipes::Pipe", "Properties": {
				"Source": "arn:aws:sqs:us-east-1:000000000000:src",
				"Target": "arn:aws:sqs:us-east-1:000000000000:dst",
				"RoleArn": "arn:aws:iam::000000000000:role/pipes"}}`,
		},
		{
			name: "AWS::WAFv2::WebACL",
			resource: `{"Type": "AWS::WAFv2::WebACL", "Properties": {
				"Scope": "REGIONAL", "DefaultAction": {"Allow": {}},
				"VisibilityConfig": {"CloudWatchMetricsEnabled": false, "MetricName": "m",
					"SampledRequestsEnabled": false}}}`,
			// Ref is "<scope>|<id>"-shaped here, so only the name shows the
			// duplicate. AWS answers a repeated web ACL name with
			// WAFDuplicateItemException.
			names: func(t *testing.T, srv *helpers.TestServer) []string {
				return responseFieldValues(t,
					awsJSONCall(t, srv, "AWSWAF_20190729.", "ListWebACLs", "application/x-amz-json-1.1",
						map[string]any{"Scope": "REGIONAL"}), "Name")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			stackName := "twin-" + strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(tc.name, "AWS::"), "::", "-"))
			template := fmt.Sprintf(`{"Resources": {%s"First": %s, "Second": %s}}`, tc.deps, tc.resource, tc.resource)

			create := cfnQuery(t, srv, "CreateStack", url.Values{
				"StackName":    {stackName},
				"TemplateBody": {template},
			})
			defer create.Body.Close()
			helpers.AssertStatus(t, create, http.StatusOK)

			status := waitForStackStatusIn(t, srv, stackName,
				"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
			if status != "CREATE_COMPLETE" {
				t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
					status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
			}

			ids := describeStackResourceIDs(t, srv, stackName)
			if ids["First"] == "" || ids["Second"] == "" {
				t.Fatalf("missing physical IDs: %v", ids)
			}
			if ids["First"] == ids["Second"] {
				t.Errorf("both resources got the physical ID %q — they are the same resource", ids["First"])
			}

			if tc.names == nil {
				return
			}
			// And: the two resources have two names. Every generated name
			// starts with the stack name, which is what separates them from
			// any resource the emulator creates by default.
			generated := map[string]bool{}
			for _, name := range tc.names(t, srv) {
				if strings.HasPrefix(name, stackName) {
					generated[name] = true
				}
			}
			if len(generated) != 2 {
				t.Errorf("want 2 generated names, got %d: %v", len(generated), generated)
			}
		})
	}
}

// TestCreateStack_cloudTrailTrail_generatedBucketNameIsAValidS3Name covers the
// second name AWS::CloudTrail::Trail's handler mints. S3BucketName is
// Required: Yes on the trail, but a template that omits it still has to reach
// CreateTrail with something, and that something is an S3 bucket name — 3 to
// 63 characters, lowercase only, beginning and ending with a letter or a
// digit — not a trail name, which may be 128 characters and mixed case. Built
// from the stack name alone it was also the same bucket for every trail in the
// stack.
func TestCreateStack_cloudTrailTrail_generatedBucketNameIsAValidS3Name(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "trail-default-bucket"

	create := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName": {stackName},
		"TemplateBody": {`{"Resources": {
			"FirstTrail": {"Type": "AWS::CloudTrail::Trail", "Properties": {}},
			"SecondTrail": {"Type": "AWS::CloudTrail::Trail", "Properties": {}}}}`},
	})
	defer create.Body.Close()
	helpers.AssertStatus(t, create, http.StatusOK)

	status := waitForStackStatusIn(t, srv, stackName,
		"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
	if status != "CREATE_COMPLETE" {
		t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
			status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
	}

	buckets := responseFieldValues(t, cloudtrailJSONCall(t, srv, "DescribeTrails", map[string]any{}), "S3BucketName")
	generated := map[string]bool{}
	for _, bucket := range buckets {
		if !strings.HasPrefix(bucket, stackName) {
			continue
		}
		generated[bucket] = true
		if n := len(bucket); n < 3 || n > 63 {
			t.Errorf("generated bucket name %q is %d characters, outside S3's 3-63", bucket, n)
		}
		if !regexp.MustCompile(charsetS3Bucket).MatchString(bucket) {
			t.Errorf("generated bucket name %q is not a valid S3 bucket name", bucket)
		}
	}
	if len(generated) != 2 {
		t.Errorf("want a bucket name per trail, got %d: %v", len(generated), generated)
	}
}

// arnResourceName returns the part of an ARN after its last colon — the
// resource name for the "arn:partition:service:region:account:name"-shaped
// ARNs SQS and SNS return as a Create handler's physical ID.
func arnResourceName(arn string) string {
	if i := strings.LastIndex(arn, ":"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// physicalIDIsName is the physicalName extractor for a handler whose physical
// ID is already the service-visible name, with nothing to unwrap.
func physicalIDIsName(physicalID string) string { return physicalID }

// iamARNName returns the entity name from an IAM ARN, whose resource part is
// "{type}/{name}" — a slash, not the colon arnResourceName splits on.
func iamARNName(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// lambdaLayerARNName returns the layer name from a layer VERSION ARN, whose
// last colon-separated segment is the version number and not the name:
// arn:aws:lambda:{region}:{account}:layer:{name}:{version}.
func lambdaLayerARNName(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 2 {
		return arn
	}
	return parts[len(parts)-2]
}

// TestCreateStack_generatedNameOverflow_capsAtServiceLimit pins #1691: a
// generated name ("{StackName}-{LogicalID}-{RANDOM}") is only ever capped at
// generatedName's 255-character default unless a handler explicitly asks for
// its service's own limit. A long stack name plus logical ID overflows a
// tighter limit — S3's 63, SQS's 80, Lambda's 64, IAM's 64 — well before it
// overflows 255, minting a name the real service would reject with
// InvalidBucketName, InvalidParameterValue, or the equivalent, even though
// Overcast itself accepted it.
func TestCreateStack_generatedNameOverflow_capsAtServiceLimit(t *testing.T) {
	// Given: a stack name and logical ID long enough together (141 chars) to
	// overflow every per-service cap below, chosen from characters valid in
	// every one of those services' name grammars.
	longStack := strings.Repeat("a", 70)
	longLogical := strings.Repeat("B", 70)

	for _, tc := range []struct {
		name       string
		slug       string // stack-name-safe identifier for this case
		logicalID  string
		properties string
		maxLen     int
		lowercase  bool
		fifo       bool
		// stackQualified marks a handler whose physical ID is
		// "{StackName}-{name}" rather than the name itself, so the name under
		// test has to be recovered before it is measured.
		stackQualified bool
		// extraResources are sibling resources the case's own resource needs
		// to provision at all, as a JSON fragment of `"Logical": {...}` pairs
		// appended after it. AWS::IAM::Policy is the case this exists for: it
		// has to name a principal, and that principal has to exist.
		extraResources string
		// physicalName extracts the service-visible name from the
		// PhysicalResourceId DescribeStackResources reports.
		physicalName func(physicalID string) string
	}{
		{
			name:         "AWS::S3::Bucket",
			slug:         "s3-bucket",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::S3::Bucket", "Properties": {}}`,
			maxLen:       63,
			lowercase:    true,
			physicalName: physicalIDIsName,
		},
		{
			name:         "AWS::SQS::Queue",
			slug:         "sqs-queue",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::SQS::Queue", "Properties": {}}`,
			maxLen:       80,
			physicalName: arnResourceName,
		},
		{
			name:         "AWS::SQS::Queue FIFO",
			slug:         "sqs-queue-fifo",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::SQS::Queue", "Properties": {"FifoQueue": true}}`,
			maxLen:       80,
			fifo:         true,
			physicalName: arnResourceName,
		},
		{
			name:         "AWS::SNS::Topic",
			slug:         "sns-topic",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::SNS::Topic", "Properties": {}}`,
			maxLen:       256,
			physicalName: arnResourceName,
		},
		{
			name:      "AWS::Lambda::Function",
			slug:      "lambda-function",
			logicalID: longLogical,
			properties: `{
        "Type": "AWS::Lambda::Function",
        "Properties": {
          "Runtime": "nodejs20.x",
          "Handler": "index.handler",
          "Role": "arn:aws:iam::000000000000:role/lambda-role",
          "Code": {"ZipFile": "exports.handler = async () => ({});"}
        }
      }`,
			maxLen:       64,
			physicalName: physicalIDIsName,
		},
		{
			name:      "AWS::IAM::Role",
			slug:      "iam-role",
			logicalID: longLogical,
			properties: `{
        "Type": "AWS::IAM::Role",
        "Properties": {
          "AssumeRolePolicyDocument": {
            "Version": "2012-10-17",
            "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}]
          }
        }
      }`,
			maxLen:       64,
			physicalName: physicalIDIsName,
		},
		{
			name:         "AWS::Kinesis::Stream",
			slug:         "kinesis-stream",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::Kinesis::Stream", "Properties": {"ShardCount": 1}}`,
			maxLen:       128,
			physicalName: physicalIDIsName,
		},
		{
			name:      "AWS::IAM::Policy",
			slug:      "iam-policy",
			logicalID: longLogical,
			properties: `{
        "Type": "AWS::IAM::Policy",
        "Properties": {
          "PolicyDocument": {
            "Version": "2012-10-17",
            "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "*"}]
          },
          "Users": [{"Ref": "PolicyHolder"}]
        }
      }`,
			extraResources: `"PolicyHolder": {"Type": "AWS::IAM::User", "Properties": {"UserName": "generated-name-policy-holder"}}`,
			maxLen:         128,
			// The handler mints "{StackName}-{PolicyName}" as the physical ID;
			// IAM only ever sees the second half.
			stackQualified: true,
			physicalName:   physicalIDIsName,
		},
		{
			name:      "AWS::IAM::ManagedPolicy",
			slug:      "iam-managed-policy",
			logicalID: longLogical,
			properties: `{
        "Type": "AWS::IAM::ManagedPolicy",
        "Properties": {
          "PolicyDocument": {
            "Version": "2012-10-17",
            "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "*"}]
          }
        }
      }`,
			maxLen:       128,
			physicalName: iamARNName,
		},
		{
			name:       "AWS::IAM::InstanceProfile",
			slug:       "iam-instance-profile",
			logicalID:  longLogical,
			properties: `{"Type": "AWS::IAM::InstanceProfile", "Properties": {}}`,
			maxLen:     128,
			// Ref on AWS::IAM::InstanceProfile is the profile name, so the
			// physical ID is already the name — the ARN is GetAtt "Arn".
			physicalName: physicalIDIsName,
		},
		{
			name:         "AWS::IAM::Group",
			slug:         "iam-group",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::IAM::Group", "Properties": {}}`,
			maxLen:       128,
			physicalName: physicalIDIsName,
		},
		{
			name:         "AWS::Lambda::LayerVersion",
			slug:         "lambda-layer-version",
			logicalID:    longLogical,
			properties:   `{"Type": "AWS::Lambda::LayerVersion", "Properties": {"Content": {"ZipFile": "dGVzdA=="}}}`,
			maxLen:       140,
			physicalName: lambdaLayerARNName,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			stackName := longStack + "-" + tc.slug
			resources := fmt.Sprintf("%q: %s", tc.logicalID, tc.properties)
			if tc.extraResources != "" {
				resources += ", " + tc.extraResources
			}
			template := fmt.Sprintf(`{"Resources": {%s}}`, resources)

			create := cfnQuery(t, srv, "CreateStack", url.Values{
				"StackName":    {stackName},
				"TemplateBody": {template},
			})
			defer create.Body.Close()
			helpers.AssertStatus(t, create, http.StatusOK)

			status := waitForStackStatusIn(t, srv, stackName,
				"CREATE_COMPLETE", "CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_IN_PROGRESS")
			if status != "CREATE_COMPLETE" {
				t.Fatalf("stack status = %s, want CREATE_COMPLETE; reasons:\n%s",
					status, strings.Join(describeStackEventReasons(t, srv, stackName), "\n"))
			}

			physID := describeStackResourceIDs(t, srv, stackName)[tc.logicalID]
			if physID == "" {
				t.Fatal("resource has no physical ID")
			}
			// A stack-qualified physical ID is CloudFormation's bookkeeping, not
			// the name the service received — measure the name, not the prefix.
			if tc.stackQualified {
				physID = strings.TrimPrefix(physID, stackName+"-")
			}
			got := tc.physicalName(physID)

			// Then: the generated name respects the service's own limit, not
			// just generatedName's 255-character default.
			if len(got) > tc.maxLen {
				t.Errorf("generated name %q is %d characters, want at most %d (service limit)", got, len(got), tc.maxLen)
			}
			// And not materially shorter than the limit either. Without this,
			// a handler wired to another service's smaller constant still
			// satisfies the bound above while truncating names the service
			// would have accepted: that is how the IAM group handler was
			// found sharing the 64-character role constant when IAM allows a
			// group 128.
			//
			// Only meaningful where the name is actually truncated. The
			// untruncated form is the stack name, the logical ID and
			// CloudFormation's random suffix joined by hyphens; SNS's 256 is
			// roomy enough that these fixtures fit inside it untouched, so
			// there the cap never engages. Where it does engage the result
			// lands exactly on the limit, bar one character when the cut
			// falls on a hyphen and generatedNameWithin trims it — the base
			// never holds two in a row, so it can never lose more.
			const randomSuffixLen = 12
			untruncated := len(stackName) + 1 + len(tc.logicalID) + 1 + randomSuffixLen
			if untruncated > tc.maxLen && len(got) < tc.maxLen-1 {
				t.Errorf("generated name %q is %d characters, want %d (the service limit) — truncated more than the service requires",
					got, len(got), tc.maxLen)
			}
			if tc.lowercase && got != strings.ToLower(got) {
				t.Errorf("generated name %q is not lowercase", got)
			}
			if strings.HasSuffix(got, "-") {
				t.Errorf("generated name %q ends with a hyphen", got)
			}
			if tc.fifo && !strings.HasSuffix(got, ".fifo") {
				t.Errorf("generated FIFO queue name %q does not end with .fifo", got)
			}
		})
	}
}
