/**
 * clients.ts — AWS SDK v3 client factory for the compat Node.js suite.
 *
 * All clients point at the Overcast emulator. Credentials are fixed to
 * "overcast"/"overcast" — the emulator accepts any non-empty values.
 *
 * Add new client methods here when adding new service groups.
 */

import { S3Client } from "@aws-sdk/client-s3"
import { SQSClient } from "@aws-sdk/client-sqs"
import { SNSClient } from "@aws-sdk/client-sns"
import { DynamoDBClient } from "@aws-sdk/client-dynamodb"
import { LambdaClient } from "@aws-sdk/client-lambda"
import { CloudWatchLogsClient } from "@aws-sdk/client-cloudwatch-logs"
import { SESClient } from "@aws-sdk/client-ses"
import { IAMClient } from "@aws-sdk/client-iam"
import { STSClient } from "@aws-sdk/client-sts"
import { SecretsManagerClient } from "@aws-sdk/client-secrets-manager"
import { KMSClient } from "@aws-sdk/client-kms"
import { SSMClient } from "@aws-sdk/client-ssm"
import { KinesisClient } from "@aws-sdk/client-kinesis"
import { EventBridgeClient } from "@aws-sdk/client-eventbridge"
import { PipesClient } from "@aws-sdk/client-pipes"
import { CloudFormationClient } from "@aws-sdk/client-cloudformation"
import { EC2Client } from "@aws-sdk/client-ec2"
import { ECSClient } from "@aws-sdk/client-ecs"
import { CognitoIdentityProviderClient } from "@aws-sdk/client-cognito-identity-provider"
import { AppSyncClient } from "@aws-sdk/client-appsync"
import { APIGatewayClient } from "@aws-sdk/client-api-gateway"
import { ApiGatewayV2Client } from "@aws-sdk/client-apigatewayv2"
import { RDSClient } from "@aws-sdk/client-rds"
import { ElastiCacheClient } from "@aws-sdk/client-elasticache"
import { EFSClient } from "@aws-sdk/client-efs"
import { SFNClient } from "@aws-sdk/client-sfn"
import { WAFV2Client } from "@aws-sdk/client-wafv2"
import { ShieldClient } from "@aws-sdk/client-shield"
import { GlueClient } from "@aws-sdk/client-glue"
import { AthenaClient } from "@aws-sdk/client-athena"
import { CloudFrontClient } from "@aws-sdk/client-cloudfront"
import { ECRClient } from "@aws-sdk/client-ecr"
import { NodeHttpHandler } from "@smithy/node-http-handler"
import type { TestContext } from "./harness.ts"

const CREDENTIALS = {
  accessKeyId: "overcast",
  secretAccessKey: "overcast",
} as const

/**
 * The client configuration every group in this suite uses — endpoint,
 * credentials, region and the HTTP/1.1 handler, and nothing else.
 *
 * Exported so the scenario interpreter (lib/scenario/client.ts), which builds
 * its clients by dynamic import rather than from the map below, configures
 * them identically instead of keeping a second copy of the rules. There has to
 * be exactly one answer in this suite to "how is a client configured", or the
 * separation boundary holds only for the groups that import this file.
 */
export function clientConfig(ctx: Pick<TestContext, "endpoint" | "region">) {
  return {
    endpoint: ctx.endpoint,
    region: ctx.region,
    credentials: CREDENTIALS,
    // Force HTTP/1.1 — Overcast runs plain HTTP without TLS, so HTTP/2 (which
    // requires ALPN negotiation) is unavailable. Without this, the SDK may
    // attempt NodeHttp2Handler and fail with ERR_HTTP2_ERROR during setup.
    requestHandler: new NodeHttpHandler(),
    tls: false,
  } as const
}

export interface Clients {
  s3: S3Client
  sqs: SQSClient
  sns: SNSClient
  dynamodb: DynamoDBClient
  lambda: LambdaClient
  logs: CloudWatchLogsClient
  ses: SESClient
  iam: IAMClient
  sts: STSClient
  secretsmanager: SecretsManagerClient
  kms: KMSClient
  ssm: SSMClient
  kinesis: KinesisClient
  eventbridge: EventBridgeClient
  pipes: PipesClient
  cloudformation: CloudFormationClient
  ec2: EC2Client
  ecs: ECSClient
  cognito: CognitoIdentityProviderClient
  appsync: AppSyncClient
  apigateway: APIGatewayClient
  apigatewayv2: ApiGatewayV2Client
  rds: RDSClient
  elasticache: ElastiCacheClient
  efs: EFSClient
  sfn: SFNClient
  wafv2: WAFV2Client
  shield: ShieldClient
  glue: GlueClient
  athena: AthenaClient
  cloudfront: CloudFrontClient
  ecr: ECRClient
}

/**
 * Create all SDK clients pointing at the given endpoint.
 *
 * Clients are lightweight and cheap to construct — no pooling or caching
 * is needed for test suites.
 */
export function makeClients(ctx: Pick<TestContext, "endpoint" | "region">): Clients {
  const cfg = clientConfig(ctx)
  return {
    s3: new S3Client({ ...cfg, forcePathStyle: true }),
    sqs: new SQSClient(cfg),
    sns: new SNSClient(cfg),
    dynamodb: new DynamoDBClient(cfg),
    lambda: new LambdaClient(cfg),
    logs: new CloudWatchLogsClient(cfg),
    ses: new SESClient(cfg),
    iam: new IAMClient(cfg),
    sts: new STSClient(cfg),
    secretsmanager: new SecretsManagerClient(cfg),
    kms: new KMSClient(cfg),
    ssm: new SSMClient(cfg),
    kinesis: new KinesisClient(cfg),
    eventbridge: new EventBridgeClient(cfg),
    pipes: new PipesClient(cfg),
    cloudformation: new CloudFormationClient(cfg),
    ec2: new EC2Client(cfg),
    ecs: new ECSClient(cfg),
    cognito: new CognitoIdentityProviderClient(cfg),
    appsync: new AppSyncClient(cfg),
    apigateway: new APIGatewayClient(cfg),
    apigatewayv2: new ApiGatewayV2Client(cfg),
    rds: new RDSClient(cfg),
    elasticache: new ElastiCacheClient(cfg),
    efs: new EFSClient(cfg),
    sfn: new SFNClient(cfg),
    wafv2: new WAFV2Client(cfg),
    shield: new ShieldClient(cfg),
    glue: new GlueClient(cfg),
    athena: new AthenaClient(cfg),
    cloudfront: new CloudFrontClient(cfg),
    ecr: new ECRClient(cfg),
  }
}
