/**
 * Where in the console an AWS resource lives: ARN (or service + id) → route.
 * `ArnLink`, `ResourceLink` and `LinkifiedText` in `./arn-link` render these;
 * this module is the resolution alone, so non-component code can ask whether
 * an ARN has a page without importing a component file.
 */

// ─── Route resolution ────────────────────────────────────────────────

export type ResolvedRoute =
  | { kind: "params"; to: string; params: Record<string, string>; search?: Record<string, string> }
  | { kind: "search"; to: string; search: Record<string, string> }

/** The IAM page's tab for each kind of IAM resource ARN. */
const IAM_TAB_FOR: Record<string, string> = {
  role: "roles",
  user: "users",
  group: "groups",
  policy: "policies",
}

/**
 * The Athena page's tabs, as its `tab` search param names them — the ones a
 * link to one Athena resource opens on, filtered to it by `q`.
 */
export const ATHENA_TAB = {
  workgroups: "workgroups",
  savedQueries: "saved-queries",
  dataCatalogs: "data-catalogs",
} as const

/** The Athena page's tab for each kind of Athena resource ARN. */
const ATHENA_TAB_FOR: Record<string, string> = {
  workgroup: ATHENA_TAB.workgroups,
  datacatalog: ATHENA_TAB.dataCatalogs,
}

/** The Athena page, on the tab listing `kind`, filtered to `name`. */
function athenaRoute(kind: string, name: string): ResolvedRoute {
  return { kind: "search", to: "/athena", search: { tab: ATHENA_TAB_FOR[kind], q: name } }
}

/** Resolve an ARN to a UI route. Returns null for unknown/unparseable ARNs. */
export function resolveArn(arn: string): ResolvedRoute | null {
  if (!arn.startsWith("arn:")) return null
  const parts = arn.split(":")
  if (parts.length < 6) return null

  const service = parts[2]

  switch (service) {
    case "sqs": {
      const queue = parts[5]
      if (queue) return { kind: "params", to: "/sqs/$queue", params: { queue } }
      break
    }
    case "sns": {
      // subscription ARNs have 7+ parts — no dedicated page
      if (parts.length === 6) {
        const topic = parts[5]
        if (topic) return { kind: "params", to: "/sns/$topic", params: { topic } }
      }
      break
    }
    case "lambda": {
      const resourceType = parts[5]
      if (resourceType === "function") {
        const name = parts[6]
        if (name) return { kind: "params", to: "/lambda/$name", params: { name } }
      } else if (resourceType === "layer") {
        const layerName = parts[6]
        if (layerName)
          return { kind: "params", to: "/lambda/layers/$layerName", params: { layerName } }
      }
      break
    }
    case "dynamodb": {
      const tableMatch = parts.at(5)?.match(/^table\/([^/]+)/)
      if (tableMatch)
        return { kind: "params", to: "/dynamodb/$tableName", params: { tableName: tableMatch[1] } }
      break
    }
    case "s3": {
      const bucket = parts[5]
      if (bucket) return { kind: "params", to: "/s3/$bucket", params: { bucket } }
      break
    }
    case "logs": {
      if (parts[5] === "log-group") {
        const rest = parts.slice(6).join(":")
        const stream = /^(.+?):log-stream:(.+)$/.exec(rest)
        if (stream)
          return {
            kind: "search",
            to: "/cloudwatch/logs/stream",
            search: { groupName: stream[1], streamName: stream[2] },
          }
        const groupName = rest.replace(/:\*$/, "")
        if (groupName)
          return { kind: "search", to: "/cloudwatch/logs/group", search: { groupName } }
      }
      break
    }
    case "states": {
      // stateMachine:name[:version-or-alias]
      if (parts[5] === "stateMachine" && parts[6])
        return { kind: "params", to: "/stepfunctions/$name", params: { name: parts[6] } }
      // execution:machine:execution — a distributed Map's child executions
      // name the machine "machine/label", and only their ARN finds them.
      if (parts[5] === "execution" && parts[6] && parts[7]) {
        const [name] = parts[6].split("/")
        return {
          kind: "params",
          to: "/stepfunctions/execution/$name/$execution",
          params: { name, execution: parts[7] },
          ...(parts[6].includes("/") ? { search: { arn } } : {}),
        }
      }
      break
    }
    case "iam": {
      // arn:aws:iam::account:role/path/name — the list filters by the name.
      const iamMatch = parts.at(5)?.match(/^(role|user|group|policy)\/(?:.*\/)?([^/]+)$/)
      if (iamMatch)
        return {
          kind: "search",
          to: "/iam",
          search: { tab: IAM_TAB_FOR[iamMatch[1]], q: iamMatch[2] },
        }
      break
    }
    case "cloudformation": {
      const stackMatch = parts.at(5)?.match(/^stack\/([^/]+)/)
      if (stackMatch)
        return {
          kind: "params",
          to: "/cloudformation/$stackName",
          params: { stackName: stackMatch[1] },
        }
      break
    }
    case "secretsmanager": {
      // arn:aws:secretsmanager:region:account:secret:name-suffix
      const secretName = parts[6] ?? parts[5]
      if (secretName)
        return { kind: "params", to: "/secretsmanager/$secretName", params: { secretName } }
      break
    }
    case "kinesis": {
      // arn:aws:kinesis:region:account:stream/name
      const streamMatch = parts.at(5)?.match(/^stream\/(.+)/)
      if (streamMatch)
        return {
          kind: "params",
          to: "/kinesis/$streamName",
          params: { streamName: streamMatch[1] },
        }
      break
    }
    case "ssm": {
      // arn:aws:ssm:region:account:parameter/name
      const paramMatch = parts.at(5)?.match(/^parameter\/(.+)/)
      if (paramMatch) return { kind: "params", to: "/ssm/$name", params: { name: paramMatch[1] } }
      break
    }
    case "rds": {
      const dbMatch = parts.at(5)?.match(/^db:(.+)/)
      if (dbMatch) return { kind: "params", to: "/rds/$instance", params: { instance: dbMatch[1] } }
      break
    }
    case "cognito-idp": {
      // arn:aws:cognito-idp:region:account:userpool/pool-id
      const poolMatch = parts.at(5)?.match(/^userpool\/(.+)/)
      if (poolMatch)
        return { kind: "params", to: "/cognito/$poolId", params: { poolId: poolMatch[1] } }
      break
    }
    case "appsync": {
      // arn:aws:appsync:region:account:apis/apiId
      const apiMatch = parts.at(5)?.match(/^apis\/(.+)/)
      if (apiMatch) return { kind: "params", to: "/appsync/$apiId", params: { apiId: apiMatch[1] } }
      break
    }
    case "events": {
      // arn:aws:events:region:account:event-bus/name
      const busMatch = parts.at(5)?.match(/^event-bus\/(.+)/)
      if (busMatch)
        return { kind: "params", to: "/eventbridge/$busName", params: { busName: busMatch[1] } }
      break
    }
    case "kms": {
      // arn:aws:kms:region:account:key/key-id
      const keyMatch = parts.at(5)?.match(/^key\/(.+)/)
      if (keyMatch) return { kind: "params", to: "/kms/$keyId", params: { keyId: keyMatch[1] } }
      break
    }
    case "ecr": {
      // arn:aws:ecr:region:account:repository/name
      const repoMatch = parts.at(5)?.match(/^repository\/(.+)/)
      if (repoMatch)
        return {
          kind: "params",
          to: "/ecr/$repositoryName",
          params: { repositoryName: repoMatch[1] },
        }
      break
    }
    case "cloudfront": {
      // arn:aws:cloudfront::account:distribution/id
      const distMatch = parts.at(5)?.match(/^distribution\/(.+)/)
      if (distMatch)
        return {
          kind: "params",
          to: "/cloudfront/$distributionId",
          params: { distributionId: distMatch[1] },
        }
      break
    }
    case "ec2": {
      // arn:aws:ec2:region:account:instance/id or vpc/id
      const instanceMatch = parts.at(5)?.match(/^instance\/(.+)/)
      if (instanceMatch)
        return {
          kind: "params",
          to: "/ec2/$instanceId",
          params: { instanceId: instanceMatch[1] },
        }
      const vpcMatch = parts.at(5)?.match(/^vpc\/(.+)/)
      if (vpcMatch) return { kind: "params", to: "/ec2/vpc/$vpcId", params: { vpcId: vpcMatch[1] } }
      break
    }
    case "ecs": {
      // arn:aws:ecs:region:account:cluster/name
      const clusterMatch = parts.at(5)?.match(/^cluster\/([^/]+)/)
      if (clusterMatch)
        return { kind: "params", to: "/ecs/$cluster", params: { cluster: clusterMatch[1] } }
      break
    }
    case "athena": {
      // arn:aws:athena:region:account:workgroup/name or datacatalog/name
      const athenaMatch = parts.at(5)?.match(/^(workgroup|datacatalog)\/(.+)$/)
      if (athenaMatch) return athenaRoute(athenaMatch[1], athenaMatch[2])
      break
    }
    case "glue": {
      // arn:aws:glue:region:account:table/db/name, database/db or catalog
      const resource = parts.at(5) ?? ""
      const tableMatch = /^table\/([^/]+)\/(.+)$/.exec(resource)
      if (tableMatch)
        return {
          kind: "params",
          to: "/glue/$database/$table",
          params: { database: tableMatch[1], table: tableMatch[2] },
        }
      const databaseMatch = /^database\/(.+)$/.exec(resource)
      if (databaseMatch)
        return { kind: "params", to: "/glue/$database", params: { database: databaseMatch[1] } }
      if (resource === "catalog") return { kind: "search", to: "/glue", search: {} }
      break
    }
    case "s3tables": {
      // arn:aws:s3tables:region:account:bucket/name or bucket/name/table/id
      const bucketMatch = parts.at(5)?.match(/^bucket\/([^/]+)(?:\/table\/([^/]+))?$/)
      if (bucketMatch?.[2])
        return {
          kind: "params",
          to: "/s3tables/$bucket/$tableId",
          params: { bucket: bucketMatch[1], tableId: bucketMatch[2] },
        }
      if (bucketMatch)
        return { kind: "params", to: "/s3tables/$bucket", params: { bucket: bucketMatch[1] } }
      break
    }
    case "apigateway": {
      // arn:aws:apigateway:region::/restapis/id or /apis/id (account segment is empty)
      const restMatch = parts.at(5)?.match(/^\/restapis\/([^/]+)/)
      if (restMatch)
        return { kind: "params", to: "/apigateway/rest/$apiId", params: { apiId: restMatch[1] } }
      const httpMatch = parts.at(5)?.match(/^\/apis\/([^/]+)/)
      if (httpMatch)
        return { kind: "params", to: "/apigateway/http/$apiId", params: { apiId: httpMatch[1] } }
      break
    }
  }
  return null
}

// Normalise CFN types ("AWS::S3::Bucket") → short service name ("s3")
const CFN_TYPE_TO_SERVICE: Record<string, string> = {
  "AWS::SQS::Queue": "sqs",
  "AWS::SNS::Topic": "sns",
  "AWS::Lambda::Function": "lambda",
  "AWS::Lambda::LayerVersion": "lambda:layer",
  "AWS::DynamoDB::Table": "dynamodb",
  "AWS::S3::Bucket": "s3",
  "AWS::Logs::LogGroup": "logs",
  "AWS::CloudWatch::LogGroup": "logs",
  "AWS::CloudFormation::Stack": "cloudformation",
  "AWS::SecretsManager::Secret": "secretsmanager",
  "AWS::Kinesis::Stream": "kinesis",
  "AWS::SSM::Parameter": "ssm",
  "AWS::RDS::DBInstance": "rds",
  "AWS::Cognito::UserPool": "cognito",
  "AWS::AppSync::GraphQLApi": "appsync",
  "AWS::Events::EventBus": "eventbridge",
  "AWS::Athena::WorkGroup": "athena:workgroup",
  "AWS::Athena::DataCatalog": "athena:datacatalog",
  "AWS::Glue::Database": "glue:database",
  // Both answer Ref with their ARN.
  "AWS::S3Tables::TableBucket": "s3tables",
  "AWS::S3Tables::Table": "s3tables",
}

/**
 * Resolve a plain resource ID + service name (or CFN type) to a UI route.
 * Returns null for unsupported services.
 */
export function resolveService(service: string, resourceId: string): ResolvedRoute | null {
  const svc = CFN_TYPE_TO_SERVICE[service] ?? service.toLowerCase()

  switch (svc) {
    case "sqs": {
      // resourceId may be a full queue URL — extract name from the last path segment
      const queue = resourceId.split("/").pop() ?? resourceId
      return { kind: "params", to: "/sqs/$queue", params: { queue } }
    }
    case "sns": {
      // resourceId may be a full ARN — extract topic name from last colon segment
      const topic = resourceId.includes("arn:")
        ? ((resolveArn(resourceId) as { params: { topic: string } } | null)?.params.topic ??
          resourceId.split(":").pop() ??
          resourceId)
        : resourceId
      return { kind: "params", to: "/sns/$topic", params: { topic } }
    }
    case "lambda":
      return { kind: "params", to: "/lambda/$name", params: { name: resourceId } }
    case "lambda:layer":
      return { kind: "params", to: "/lambda/layers/$layerName", params: { layerName: resourceId } }
    case "dynamodb":
      return { kind: "params", to: "/dynamodb/$tableName", params: { tableName: resourceId } }
    case "s3":
      return { kind: "params", to: "/s3/$bucket", params: { bucket: resourceId } }
    case "logs":
      return { kind: "search", to: "/cloudwatch/logs/group", search: { groupName: resourceId } }
    case "cloudformation":
      return {
        kind: "params",
        to: "/cloudformation/$stackName",
        params: { stackName: resourceId },
      }
    case "secretsmanager":
      return {
        kind: "params",
        to: "/secretsmanager/$secretName",
        params: { secretName: resourceId },
      }
    case "kinesis":
      return { kind: "params", to: "/kinesis/$streamName", params: { streamName: resourceId } }
    case "ssm":
      return { kind: "params", to: "/ssm/$name", params: { name: resourceId } }
    case "rds":
      return { kind: "params", to: "/rds/$instance", params: { instance: resourceId } }
    case "cognito":
      return { kind: "params", to: "/cognito/$poolId", params: { poolId: resourceId } }
    case "appsync":
      return { kind: "params", to: "/appsync/$apiId", params: { apiId: resourceId } }
    case "eventbridge":
      return { kind: "params", to: "/eventbridge/$busName", params: { busName: resourceId } }
    case "athena:workgroup":
      return athenaRoute("workgroup", resourceId)
    case "athena:datacatalog":
      return athenaRoute("datacatalog", resourceId)
    case "glue:database":
      return { kind: "params", to: "/glue/$database", params: { database: resourceId } }
    case "s3tables":
      return resolveArn(resourceId)
  }
  return null
}

// Matches ARN-shaped substrings anywhere within a larger string — e.g. an
// error message like "failed to invoke arn:aws:lambda:...:function:foo".
// The resource segment can contain colons/slashes, so this greedily
// consumes everything up to the first character that would never appear
// unescaped in one of this emulator's ARNs (whitespace or a JSON/text
// delimiter).
export const EMBEDDED_ARN_PATTERN = /arn:[a-z0-9-]+:[a-z0-9-]+:[a-z0-9-]*:\d*:[^\s"'<>,;]+/gi

/** The distinct ARNs in `text` that the console has a page for, in order of appearance. */
export function findLinkableArns(text: string): string[] {
  if (!text.includes("arn:")) return []
  const found = new Set<string>()
  for (const m of text.matchAll(EMBEDDED_ARN_PATTERN)) {
    // A JSON-quoted ARN is cut at its closing quote already; one written
    // inside brackets or a sentence can still carry the closing punctuation.
    const arn = m[0].replace(/[)\].}]+$/, "")
    if (resolveArn(arn)) found.add(arn)
  }
  return [...found]
}
