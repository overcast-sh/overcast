/**
 * Typed API client for the Hono BFF.
 *
 * Injects the current emulator endpoint as headers on every request
 * so the API server knows where to point the AWS SDK clients.
 *
 * Usage:
 *   import { s3 } from '@/services/api'
 *   const { data } = useQuery({ queryKey: ['buckets'], queryFn: () => s3.listBuckets() })
 *
 * Each service is implemented in its own module under the api/ directory
 * and re-exported here for backward compatibility.
 */

export { s3 } from "./api/s3"
export { sqs } from "./api/sqs"
export { sns } from "./api/sns"
export { dynamodb } from "./api/dynamodb"
export { lambda, lambdaInstances } from "./api/lambda"
export { pipes } from "./api/pipes"
export { ses } from "./api/ses"
export { secretsmanager } from "./api/secretsmanager"
export { logs } from "./api/logs"
export { cloudwatch } from "./api/cloudwatch"
export { kinesis } from "./api/kinesis"
export { cloudformation } from "./api/cloudformation"
export { ecs } from "./api/ecs"
export { efs } from "./api/efs"
export { ec2 } from "./api/ec2"
export { rds } from "./api/rds"
export { elasticache } from "./api/elasticache"
export { apigateway } from "./api/apigateway"
export { cloudfront } from "./api/cloudfront"
export { cognito } from "./api/cognito"
export { kms } from "./api/kms"
export { ssm } from "./api/ssm"
export { sts } from "./api/sts"
export { waf } from "./api/waf"
export { ecr } from "./api/ecr"
export { eks } from "./api/eks"
export { autoscaling } from "./api/autoscaling"
export { eventbridge } from "./api/eventbridge"
export { appregistry } from "./api/appregistry"
export { msk } from "./api/msk"
export { metrics, health, topology, inbox } from "./api/misc"
export { preflight } from "./api/preflight"
export { settings } from "./api/settings"
export { debuggerTargets } from "./api/debugger"
