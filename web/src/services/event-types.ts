/**
 * EventType — namespaced constants for all SSE event types emitted by the
 * emulator.  Mirrors the Go constants in internal/events/event.go.
 *
 * Usage:
 *   import { EventType } from "@/services/event-types"
 *   if (event.type === EventType.sqs.MessageSent) { ... }
 */

export const EventType = {
  // ── Request ─────────────────────────────────────────────────────────────
  request: {
    Received: "request:Received",
  },

  // ── Service errors ───────────────────────────────────────────────────────
  // Generic error events emitted by any service. Source identifies the origin.
  service: {
    Error: "service:Error",
  },

  // ── S3 ──────────────────────────────────────────────────────────────────
  s3: {
    ObjectCreated: "s3:ObjectCreated:*",
    ObjectRemoved: "s3:ObjectRemoved:*",
    BucketCreated: "s3:BucketCreated",
    BucketDeleted: "s3:BucketDeleted",
    NotificationConfigured: "s3:NotificationConfigured",
  },

  // ── SQS ─────────────────────────────────────────────────────────────────
  sqs: {
    QueueCreated: "sqs:QueueCreated",
    QueueDeleted: "sqs:QueueDeleted",
    QueuePurged: "sqs:QueuePurged",
    MessageSent: "sqs:MessageSent",
    MessageInflight: "sqs:MessageInflight",
    MessageVisible: "sqs:MessageVisible",
    MessageDeleted: "sqs:MessageDeleted",
    MessageDLQ: "sqs:MessageDLQ",
  },

  // ── SNS ─────────────────────────────────────────────────────────────────
  sns: {
    Published: "sns:Published",
    Notification: "sns:Notification",
    EmailDelivered: "sns:EmailDelivered",
    SMSDelivered: "sns:SMSDelivered",
    WebhookDelivered: "sns:WebhookDelivered",
    PushDelivered: "sns:PushDelivered",
    LambdaDelivered: "sns:LambdaDelivered",
    DeliveryFailed: "sns:DeliveryFailed",
    TopicCreated: "sns:TopicCreated",
    TopicDeleted: "sns:TopicDeleted",
    SubscriptionCreated: "sns:SubscriptionCreated",
    SubscriptionDeleted: "sns:SubscriptionDeleted",
  },

  // ── DynamoDB ────────────────────────────────────────────────────────────
  dynamodb: {
    Insert: "dynamodb:insert",
    Modify: "dynamodb:modify",
    Remove: "dynamodb:remove",
    // Companion to Insert/Modify/Remove: AWS StreamRecord shape for filter debugging.
    StreamRecord: "dynamodb:StreamRecord",
    TableCreated: "dynamodb:TableCreated",
    TableDeleted: "dynamodb:TableDeleted",
    StreamUpdated: "dynamodb:StreamUpdated",
    ItemMutated: "dynamodb:ItemMutated",
  },

  // ── Lambda ──────────────────────────────────────────────────────────────
  lambda: {
    FunctionCreated: "lambda:FunctionCreated",
    FunctionDeleted: "lambda:FunctionDeleted",
    FunctionUpdated: "lambda:FunctionUpdated",
    InstanceAcquired: "lambda:InstanceAcquired",
    InstanceReady: "lambda:InstanceReady",
    InstanceInitializing: "lambda:InstanceInitializing",
    InstanceReleased: "lambda:InstanceReleased",
    InstanceEvicted: "lambda:InstanceEvicted",
    ESMInvoked: "lambda:ESMInvoked",
    ESMRecordFiltered: "lambda:ESMRecordFiltered",
    ESMRecordMatched: "lambda:ESMRecordMatched",
    ImagePulling: "lambda:ImagePulling",
    ImagePullComplete: "lambda:ImagePullComplete",
  },

  // ── Pipes ───────────────────────────────────────────────────────────────
  pipes: {
    Delivered: "pipes:Delivered",
    StateChanged: "pipes:StateChanged",
  },

  // ── Inbox ────────────────────────────────────────────────────────────────
  inbox: {
    Delivered: "inbox:Delivered",
  },

  // ── CloudWatch Logs ─────────────────────────────────────────────────────
  logs: {
    LogGroupCreated: "logs:LogGroupCreated",
    LogGroupDeleted: "logs:LogGroupDeleted",
    LogStreamCreated: "logs:LogStreamCreated",
    LogStreamDeleted: "logs:LogStreamDeleted",
    LogEventsWritten: "logs:LogEventsWritten",
  },

  // ── EC2 ─────────────────────────────────────────────────────────────────
  ec2: {
    VpcCreated: "ec2:VpcCreated",
    VpcDeleted: "ec2:VpcDeleted",
    SubnetCreated: "ec2:SubnetCreated",
    SubnetDeleted: "ec2:SubnetDeleted",
    SecurityGroupCreated: "ec2:SecurityGroupCreated",
    SecurityGroupDeleted: "ec2:SecurityGroupDeleted",
    InstanceLaunched: "ec2:InstanceLaunched",
    InstanceTerminated: "ec2:InstanceTerminated",
    InstanceStarted: "ec2:InstanceStarted",
    InstanceStopped: "ec2:InstanceStopped",
  },

  // ── ECS ─────────────────────────────────────────────────────────────────
  ecs: {
    ClusterCreated: "ecs:ClusterCreated",
    ClusterDeleted: "ecs:ClusterDeleted",
    TaskDefinitionRegistered: "ecs:TaskDefinitionRegistered",
    TaskDefinitionDeregistered: "ecs:TaskDefinitionDeregistered",
    TaskStarted: "ecs:TaskStarted",
    TaskStopped: "ecs:TaskStopped",
    TaskStartFailed: "ecs:TaskStartFailed",
    ServiceCreated: "ecs:ServiceCreated",
    ServiceUpdated: "ecs:ServiceUpdated",
    ServiceDeleted: "ecs:ServiceDeleted",
  },

  // ── RDS ─────────────────────────────────────────────────────────────────
  rds: {
    InstanceCreated: "rds:InstanceCreated",
    InstanceDeleted: "rds:InstanceDeleted",
    InstanceModified: "rds:InstanceModified",
    InstanceStarted: "rds:InstanceStarted",
    InstanceStopped: "rds:InstanceStopped",
    SubnetGroupCreated: "rds:SubnetGroupCreated",
    SubnetGroupDeleted: "rds:SubnetGroupDeleted",
  },

  // ── CloudFormation ───────────────────────────────────────────────────────
  cloudformation: {
    StackCreated: "cloudformation:StackCreated",
    StackUpdated: "cloudformation:StackUpdated",
    StackDeleted: "cloudformation:StackDeleted",
    StackFailed: "cloudformation:StackFailed",
    ChangeSetCreated: "cloudformation:ChangeSetCreated",
    ChangeSetExecuted: "cloudformation:ChangeSetExecuted",
    ResourceProvisioned: "cloudformation:ResourceProvisioned",
    ResourceDeleted: "cloudformation:ResourceDeleted",
  },

  // ── CloudFront ──────────────────────────────────────────────────────────
  cloudfront: {
    DistributionCreated: "cloudfront:DistributionCreated",
    DistributionUpdated: "cloudfront:DistributionUpdated",
    DistributionDeleted: "cloudfront:DistributionDeleted",
    InvalidationCreated: "cloudfront:InvalidationCreated",
  },

  // ── API Gateway ─────────────────────────────────────────────────────────
  apigateway: {
    HttpApiCreated: "apigateway:HttpApiCreated",
    HttpApiDeleted: "apigateway:HttpApiDeleted",
    RestApiCreated: "apigateway:RestApiCreated",
    RestApiDeleted: "apigateway:RestApiDeleted",
    Deployed: "apigateway:Deployed",
    /** A request reached its usage plan's throttle or quota limit. */
    Throttled: "apigateway:Throttled",
  },

  // ── AppSync ─────────────────────────────────────────────────────────────
  appsync: {
    ApiCreated: "appsync:ApiCreated",
    ApiUpdated: "appsync:ApiUpdated",
    ApiDeleted: "appsync:ApiDeleted",
    SchemaUpdated: "appsync:SchemaUpdated",
    DataSourceCreated: "appsync:DataSourceCreated",
    DataSourceDeleted: "appsync:DataSourceDeleted",
    ResolverCreated: "appsync:ResolverCreated",
    ResolverDeleted: "appsync:ResolverDeleted",
  },

  // ── Cognito ─────────────────────────────────────────────────────────────
  cognito: {
    UserPoolCreated: "cognito:UserPoolCreated",
    UserPoolDeleted: "cognito:UserPoolDeleted",
    UserCreated: "cognito:UserCreated",
    UserDeleted: "cognito:UserDeleted",
    UserConfirmed: "cognito:UserConfirmed",
    UserUpdated: "cognito:UserUpdated",
    SignIn: "cognito:SignIn",
    SignInFailed: "cognito:SignInFailed",
    PasswordChanged: "cognito:PasswordChanged",
    SignOut: "cognito:SignOut",
    GroupCreated: "cognito:GroupCreated",
    GroupDeleted: "cognito:GroupDeleted",
    GroupUpdated: "cognito:GroupUpdated",
    GroupMembershipChanged: "cognito:GroupMembershipChanged",
    ClientCreated: "cognito:ClientCreated",
    ClientDeleted: "cognito:ClientDeleted",
  },

  // ── EventBridge ─────────────────────────────────────────────────────────
  eventbridge: {
    BusCreated: "eventbridge:BusCreated",
    BusDeleted: "eventbridge:BusDeleted",
    RuleCreated: "eventbridge:RuleCreated",
    RuleDeleted: "eventbridge:RuleDeleted",
  },

  // ── IAM ─────────────────────────────────────────────────────────────────
  iam: {
    RoleCreated: "iam:RoleCreated",
    RoleDeleted: "iam:RoleDeleted",
    UserCreated: "iam:UserCreated",
    UserDeleted: "iam:UserDeleted",
    PolicyCreated: "iam:PolicyCreated",
    PolicyDeleted: "iam:PolicyDeleted",
  },

  // ── KMS ─────────────────────────────────────────────────────────────────
  kms: {
    KeyCreated: "kms:KeyCreated",
    KeyDeleted: "kms:KeyDeleted",
    KeyStateChanged: "kms:KeyStateChanged",
  },

  // ── Secrets Manager ─────────────────────────────────────────────────────
  secretsmanager: {
    SecretCreated: "secretsmanager:SecretCreated",
    SecretUpdated: "secretsmanager:SecretUpdated",
    SecretDeleted: "secretsmanager:SecretDeleted",
    SecretRotated: "secretsmanager:SecretRotated",
  },

  // ── SES ─────────────────────────────────────────────────────────────────
  ses: {
    EmailSent: "ses:EmailSent",
    IdentityCreated: "ses:IdentityCreated",
    IdentityDeleted: "ses:IdentityDeleted",
    TemplateCreated: "ses:TemplateCreated",
    TemplateDeleted: "ses:TemplateDeleted",
  },

  // ── SSM ─────────────────────────────────────────────────────────────────
  ssm: {
    ParameterCreated: "ssm:ParameterCreated",
    ParameterUpdated: "ssm:ParameterUpdated",
    ParameterDeleted: "ssm:ParameterDeleted",
  },

  // ── Step Functions ──────────────────────────────────────────────────────
  stepfunctions: {
    StateMachineCreated: "stepfunctions:StateMachineCreated",
    StateMachineUpdated: "stepfunctions:StateMachineUpdated",
    StateMachineDeleted: "stepfunctions:StateMachineDeleted",
    ExecutionStarted: "stepfunctions:ExecutionStarted",
  },

  // ── STS ─────────────────────────────────────────────────────────────────
  sts: {
    RoleAssumed: "sts:RoleAssumed",
  },

  // ── Kinesis ─────────────────────────────────────────────────────────────
  kinesis: {
    StreamCreated: "kinesis:StreamCreated",
    StreamDeleted: "kinesis:StreamDeleted",
  },

  // ── ElastiCache ──────────────────────────────────────────────────────────
  elasticache: {
    ClusterCreated: "elasticache:ClusterCreated",
    ClusterDeleted: "elasticache:ClusterDeleted",
    ClusterModified: "elasticache:ClusterModified",
    ReplicationGroupCreated: "elasticache:ReplicationGroupCreated",
    ReplicationGroupDeleted: "elasticache:ReplicationGroupDeleted",
  },

  // ── ECR ──────────────────────────────────────────────────────────────────
  ecr: {
    RepositoryCreated: "ecr:RepositoryCreated",
    RepositoryDeleted: "ecr:RepositoryDeleted",
    ImagePushed: "ecr:ImagePushed",
  },

  // ── WAF ──────────────────────────────────────────────────────────────────
  waf: {
    WebACLCreated: "waf:WebACLCreated",
    WebACLDeleted: "waf:WebACLDeleted",
  },

  // ── Athena ───────────────────────────────────────────────────────────────
  athena: {
    QueryStateChanged: "athena:QueryStateChanged",
  },

  // ── Glue ─────────────────────────────────────────────────────────────────
  glue: {
    TableChanged: "glue:TableChanged",
    PartitionsChanged: "glue:PartitionsChanged",
  },

  // ── S3 Tables ────────────────────────────────────────────────────────────
  s3tables: {
    TableCreated: "s3tables:TableCreated",
    TableDeleted: "s3tables:TableDeleted",
    TableRenamed: "s3tables:TableRenamed",
    TableCommitted: "s3tables:TableCommitted",
  },
} as const

/**
 * Event types classified as "noise" — high-frequency, low-signal traffic
 * that would otherwise crowd out the state-change events someone debugging
 * an incident actually wants to see. Mirrors the Go classification in
 * internal/events/classify.go (IsNoise) so the server's history-buffer
 * eviction policy and the Events page's default filters stay in lockstep:
 * today that is exactly per-request HTTP telemetry, including the UI's own
 * polling and any Docker/load-balancer health checks.
 */
const NOISE_EVENT_TYPES: ReadonlySet<string> = new Set([EventType.request.Received])

/** Reports whether `type` is classified as noise. See NOISE_EVENT_TYPES. */
export function isNoiseEventType(type: string): boolean {
  return NOISE_EVENT_TYPES.has(type)
}
