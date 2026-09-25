export const DEBUG_SERVICE_LABELS: Record<string, string> = {
  acm: "ACM",
  apigw: "API Gateway",
  appconfig: "AppConfig",
  appconfigdata: "AppConfig Data",
  appregistry: "AppRegistry",
  athena: "Athena",
  s3: "S3",
  s3tables: "S3 Tables",
  sqs: "SQS",
  sns: "SNS",
  dynamodb: "DynamoDB",
  lambda: "Lambda",
  cfn: "CloudFormation",
  appsync: "AppSync",
  eb: "EventBridge",
  ecr: "ECR",
  ecs: "ECS",
  ec2: "EC2",
  eks: "EKS",
  glue: "Glue",
  elbv2: "ELBv2",
  iam: "IAM",
  kms: "KMS",
  logs: "CloudWatch Logs",
  msk: "MSK",
  rds: "RDS",
  ses: "SES",
  ssm: "SSM",
}

const SERVICE_ALIASES: Record<string, string> = {
  apigateway: "apigw",
  cloudformation: "cfn",
  eventbridge: "eb",
}

export function serviceForDebugNamespace(namespace: string): string | undefined {
  return namespaceService(namespace)
}

export function firstDebugNamespaceForService(
  service: string,
  availableNamespaces: string[],
): string {
  const canonical = SERVICE_ALIASES[service] ?? service
  return availableNamespaces.find((namespace) => namespaceService(namespace) === canonical) ?? ""
}

export function groupDebugNamespaces(
  namespaces: string[],
): { service: string; namespaces: { namespace: string; category: string }[] }[] {
  const grouped = new Map<string, string[]>()
  for (const namespace of namespaces) {
    const service = namespaceService(namespace)
    const existing = grouped.get(service) ?? []
    existing.push(namespace)
    grouped.set(service, existing)
  }
  return Array.from(grouped.entries()).map(([service, ns]) => ({
    service,
    namespaces: ns.map((namespace) => ({ namespace, category: namespaceCategory(namespace) })),
  }))
}

function namespaceService(namespace: string): string {
  const colon = namespace.indexOf(":")
  return colon >= 0 ? namespace.slice(0, colon) : namespace
}

function namespaceCategory(namespace: string): string {
  const colon = namespace.indexOf(":")
  return colon >= 0 ? namespace.slice(colon + 1) : "state"
}
