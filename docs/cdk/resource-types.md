---
title: "CDK resource type coverage"
description: "Which CloudFormation resource types Overcast provisions for real, which are recognised as stubs, and what happens to a type that is in neither list."
section: "CDK"
tags:
  - cdk
  - cloudformation
  - docs
  - resources
---

# CDK resource type coverage

Overcast's CloudFormation provisioner handles **147 resource types**: 138 have
real handlers, 8 are recognised as stubs, and one — `AWS::SES::ConfigurationSet`
— is recognised but always fails. Custom resources and nested stacks are
resolved dynamically on top of those. A [`cdk deploy`](../cdk.md) succeeds for
a stack built from the first two groups.

Read the tables by what a type creates. A real handler provisions through the
emulated service, so it creates state the ordinary AWS APIs can query and
`Fn::GetAtt` resolves against. A stub returns a synthetic physical ID and
creates nothing. `AWS::SES::ConfigurationSet` does neither: it dispatches to
SES's own `CreateConfigurationSet`, which Overcast does not implement, so the
resource fails outright rather than reporting success for something that was
never created. A type in neither table is stubbed the same way the successful
stubs are, so a template that uses one still deploys.

## Real handlers

| Service | Resource Types |
| --- | --- |
| S3 | `AWS::S3::Bucket`, `AWS::S3::BucketPolicy` |
| S3 Tables | `AWS::S3Tables::TableBucket`, `AWS::S3Tables::Namespace`, `AWS::S3Tables::Table`, `AWS::S3Tables::TableBucketPolicy`, `AWS::S3Tables::TablePolicy` |
| SQS | `AWS::SQS::Queue` |
| SNS | `AWS::SNS::Topic`, `AWS::SNS::Subscription` |
| DynamoDB | `AWS::DynamoDB::Table`, `AWS::DynamoDB::GlobalTable` |
| Lambda | `AWS::Lambda::Function`, `AWS::Lambda::Alias`, `AWS::Lambda::Url`, `AWS::Lambda::EventSourceMapping`, `AWS::Lambda::Permission`, `AWS::Lambda::LayerVersion`, `AWS::Lambda::CodeSigningConfig` |
| IAM | `AWS::IAM::Role`, `AWS::IAM::Policy`, `AWS::IAM::ManagedPolicy`, `AWS::IAM::InstanceProfile`, `AWS::IAM::ServiceLinkedRole`, `AWS::IAM::User`, `AWS::IAM::Group`, `AWS::IAM::AccessKey` |
| EC2 / VPC | `AWS::EC2::VPC`, `AWS::EC2::Subnet`, `AWS::EC2::SecurityGroup`, `AWS::EC2::InternetGateway`, `AWS::EC2::VPNGateway`, `AWS::EC2::VPCGatewayAttachment`, `AWS::EC2::RouteTable`, `AWS::EC2::Route`, `AWS::EC2::SubnetRouteTableAssociation`, `AWS::EC2::NatGateway`, `AWS::EC2::EIP`, `AWS::EC2::LaunchTemplate` |
| ECS | `AWS::ECS::Cluster`, `AWS::ECS::TaskDefinition`, `AWS::ECS::Service` |
| ECR | `AWS::ECR::Repository` |
| API Gateway | `AWS::ApiGateway::RestApi`, `AWS::ApiGateway::Resource`, `AWS::ApiGateway::Method`, `AWS::ApiGateway::Deployment`, `AWS::ApiGateway::Stage`, `AWS::ApiGateway::ApiKey`, `AWS::ApiGateway::UsagePlan`, `AWS::ApiGateway::UsagePlanKey`, `AWS::ApiGateway::Authorizer`, `AWS::ApiGateway::Model`, `AWS::ApiGateway::RequestValidator` |
| API Gateway V2 | `AWS::ApiGatewayV2::Api`, `AWS::ApiGatewayV2::Stage`, `AWS::ApiGatewayV2::Integration`, `AWS::ApiGatewayV2::Route` |
| AppSync | `AWS::AppSync::Api`, `AWS::AppSync::GraphQLApi`, `AWS::AppSync::GraphQLSchema`, `AWS::AppSync::ChannelNamespace`, `AWS::AppSync::ApiKey`, `AWS::AppSync::DataSource`, `AWS::AppSync::Resolver`, `AWS::AppSync::FunctionConfiguration`, `AWS::AppSync::DomainName`, `AWS::AppSync::DomainNameApiAssociation`, `AWS::AppSync::ApiCache`, `AWS::AppSync::SourceApiAssociation` |
| AppConfig | `AWS::AppConfig::Application`, `AWS::AppConfig::Environment`, `AWS::AppConfig::ConfigurationProfile` |
| RDS | `AWS::RDS::DBInstance`, `AWS::RDS::DBCluster`, `AWS::RDS::DBSubnetGroup`, `AWS::RDS::DBParameterGroup` |
| ElastiCache | `AWS::ElastiCache::CacheCluster`, `AWS::ElastiCache::ServerlessCache`, `AWS::ElastiCache::ReplicationGroup`, `AWS::ElastiCache::SubnetGroup` |
| EFS | `AWS::EFS::FileSystem`, `AWS::EFS::MountTarget`, `AWS::EFS::AccessPoint` |
| EKS | `AWS::EKS::Cluster`, `AWS::EKS::Nodegroup`, `AWS::EKS::FargateProfile`, `AWS::EKS::Addon`, `AWS::EKS::AccessEntry`, `AWS::EKS::PodIdentityAssociation` |
| MSK | `AWS::MSK::Cluster`, `AWS::MSK::Configuration` |
| EventBridge | `AWS::Events::EventBus`, `AWS::Events::Rule` |
| Scheduler | `AWS::Scheduler::Schedule`, `AWS::Scheduler::ScheduleGroup` |
| Pipes | `AWS::Pipes::Pipe` |
| Step Functions | `AWS::StepFunctions::StateMachine`, `AWS::StepFunctions::StateMachineVersion`, `AWS::StepFunctions::StateMachineAlias`, `AWS::StepFunctions::Activity` |
| Kinesis | `AWS::Kinesis::Stream` |
| Firehose | `AWS::KinesisFirehose::DeliveryStream` |
| CloudWatch | `AWS::CloudWatch::Alarm` |
| CloudWatch Logs | `AWS::Logs::LogGroup`, `AWS::Logs::LogStream`, `AWS::Logs::MetricFilter` |
| KMS | `AWS::KMS::Key`, `AWS::KMS::Alias` |
| SSM | `AWS::SSM::Parameter` |
| Secrets Manager | `AWS::SecretsManager::Secret` |
| Cognito | `AWS::Cognito::UserPool`, `AWS::Cognito::UserPoolClient` |
| Route 53 | `AWS::Route53::HostedZone`, `AWS::Route53::RecordSet`, `AWS::Route53::HealthCheck` |
| CloudFront | `AWS::CloudFront::Distribution` |
| ELBv2 | `AWS::ElasticLoadBalancingV2::LoadBalancer`, `AWS::ElasticLoadBalancingV2::TargetGroup`, `AWS::ElasticLoadBalancingV2::Listener` |
| Auto Scaling | `AWS::AutoScaling::AutoScalingGroup`, `AWS::AutoScaling::LaunchConfiguration` |
| SES | `AWS::SES::Template` |
| ACM | `AWS::CertificateManager::Certificate` |
| CloudTrail | `AWS::CloudTrail::Trail` |
| Backup | `AWS::Backup::BackupVault`, `AWS::Backup::BackupPlan` |
| Transfer Family | `AWS::Transfer::Server`, `AWS::Transfer::User` |
| Glue | `AWS::Glue::Database`, `AWS::Glue::Table`, `AWS::Glue::Partition` |
| Athena | `AWS::Athena::WorkGroup`, `AWS::Athena::NamedQuery`, `AWS::Athena::PreparedStatement`, `AWS::Athena::DataCatalog` |
| OpenSearch | `AWS::OpenSearchService::Domain` |
| Shield | `AWS::Shield::Protection` |
| WAF v2 | `AWS::WAFv2::WebACL` |
| AppRegistry | `AWS::ServiceCatalogAppRegistry::Application`, `AWS::ServiceCatalogAppRegistry::ResourceAssociation` |
| CloudFormation | `AWS::CloudFormation::Stack` (nested stacks), `AWS::CloudFormation::CustomResource`, `Custom::*` (resolved dynamically, in addition to the 145 static handlers) |

## Stubs

Recognised, and answered with a synthetic physical ID so the stack can complete.
No real resources are created.

- `AWS::SQS::QueuePolicy`
- `AWS::ApiGateway::Account`
- `AWS::ApiGatewayV2::Deployment`
- `AWS::ElastiCache::ParameterGroup`
- `AWS::Events::Connection`
- `AWS::CDK::Metadata`
- `AWS::CloudFormation::WaitConditionHandle`
- `AWS::CloudFormation::WaitCondition`

## Recognised, but fails

`AWS::SES::ConfigurationSet` dispatches to SES's own `CreateConfigurationSet`,
which Overcast's SES emulation does not implement — see its status on the
[SES operation support](../services/ses/operations.md) page. Rather than
answering a synthetic success the way the stubs above do, the resource fails
with a `ResourceStatusReason` naming the operation — see
[CDK troubleshooting](./troubleshooting.md) for what to do with a failed
resource.

## Unknown resource types

A type in neither table is handled permissively: it receives a synthetic
physical ID (`<stackName>-<logicalId>-stub`) and succeeds. A template with
unsupported types deploys, and those resources have no backing state — see
[CDK limitations § Partial resource coverage](./limitations.md#partial-resource-coverage)
for how to see which resources were stubbed.

## `Fn::GetAtt`

For a provisioned resource, `Fn::GetAtt` resolves to the real attribute value:
`!GetAtt MyVPC.VpcId` returns the VPC ID the EC2 service created. The supported
attributes per resource type are listed in the
[CloudFormation service reference](../services/cloudformation.md).

## Related

- [CDK limitations](./limitations.md) — what a deploy does not do, resource coverage included
- [CDK troubleshooting](./troubleshooting.md) — a deploy that fails or never completes
- [Local VPCs for CDK](./local-vpc.md) — the VPC pattern that survives a teardown
- [Using AWS CDK](../cdk.md) — bootstrap and deploy against Overcast
- [CloudFormation service reference](../services/cloudformation.md) — the provisioner behind these types
- [Reference index](../README.md) — every guide and service page
