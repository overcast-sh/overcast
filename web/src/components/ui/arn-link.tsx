/**
 * ArnLink / ResourceLink — link a resource to its detail page.
 *
 * `ArnLink`      — takes a raw ARN; service and resource extracted automatically.
 * `ResourceLink` — takes either an ARN (auto-detected) or a service + resourceId pair.
 *                  Accepts both short service names ("sqs") and CFN resource types
 *                  ("AWS::SQS::Queue") so CloudFormation stacks and other callers
 *                  don't need a mapping layer.
 *
 * Supported services / CFN types → route:
 *   sqs          / AWS::SQS::Queue            → /sqs/$queue
 *   sns          / AWS::SNS::Topic            → /sns/$topic
 *   lambda       / AWS::Lambda::Function      → /lambda/$name
 *   lambda layer / AWS::Lambda::LayerVersion  → /lambda/layers/$layerName
 *   dynamodb     / AWS::DynamoDB::Table       → /dynamodb/$tableName
 *   s3           / AWS::S3::Bucket            → /s3/$bucket
 *   logs         / AWS::Logs::LogGroup        → /cloudwatch/logs/group?groupName=…
 *   logs (stream ARN)                         → /cloudwatch/logs/stream?groupName=…&streamName=…
 *   states (state machine)                    → /stepfunctions/$name
 *   states (execution)                        → /stepfunctions/execution/$name/$execution
 *   iam (role, user, group, policy)           → /iam?tab=…&q=name
 *   cloudformation / AWS::CloudFormation::Stack → /cloudformation/$stackName
 *   secretsmanager / AWS::SecretsManager::Secret → /secretsmanager/$secretName
 *   kinesis      / AWS::Kinesis::Stream       → /kinesis/$streamName
 *   ssm          / AWS::SSM::Parameter        → /ssm/$name
 *   rds          / AWS::RDS::DBInstance       → /rds/$instance
 *   cognito      / AWS::Cognito::UserPool     → /cognito/$poolId
 *   appsync      / AWS::AppSync::GraphQLApi   → /appsync/$apiId
 *   eventbridge  / AWS::Events::EventBus      → /eventbridge/$busName
 *   kms          / AWS::KMS::Key              → /kms/$keyId
 *   ecr          / AWS::ECR::Repository       → /ecr/$repositoryName
 *   cloudfront   / AWS::CloudFront::Distribution → /cloudfront/$distributionId
 *   ec2 (instance) / AWS::EC2::Instance       → /ec2/$instanceId
 *   ec2 (vpc)    / AWS::EC2::VPC              → /ec2/vpc/$vpcId
 *   ecs          / AWS::ECS::Cluster          → /ecs/$cluster
 *   apigateway (rest) / AWS::ApiGateway::RestApi → /apigateway/rest/$apiId
 *   apigateway (http) / AWS::ApiGatewayV2::Api   → /apigateway/http/$apiId
 *
 * Unrecognised services render as plain text — no link, no error.
 */
import { Fragment } from "react"
import type { MouseEventHandler, ReactNode } from "react"
import { Link } from "@tanstack/react-router"
import { cn } from "@/lib/utils"
import { endpointStore } from "@/services/endpoint-store"
import { EMBEDDED_ARN_PATTERN, resolveArn, resolveService, type ResolvedRoute } from "./arn-routes"

/** The design's link hover: accent at rest, the brighter accent-glow on hover. */
const LINK_CLASS = "text-accent transition-colors hover:text-accent-hover hover:underline"

// ─── ArnText ─────────────────────────────────────────────────────────────────

interface ArnTextProps {
  arn: string
  /** Extra Tailwind classes merged onto the base font-mono/text-xs. */
  className?: string
}

/**
 * Renders an ARN string as monospace text with word-break opportunities
 * inserted after every `:` and `/`. This lets the browser wrap at natural
 * ARN segment boundaries without corrupting the text content — `<wbr>` is
 * not included when the text is selected and copied.
 */
export function ArnText({ arn, className }: ArnTextProps) {
  // Split on `:` and `/`, keeping each delimiter in the token stream.
  // Fragment has no DOM presence, so the only extra elements emitted are
  // the `<wbr>` hints — one per `:` or `/`.
  const tokens = arn.split(/([:/])/)
  return (
    // break-normal resets any inherited word-break:break-all from a parent
    // container so that <wbr> hints are respected. [overflow-wrap:anywhere]
    // still allows a break mid-segment as a last resort if a segment is too
    // long to fit on one line.
    <span className={cn("font-mono text-xs break-normal [overflow-wrap:anywhere]", className)}>
      {tokens.map((token, i) =>
        token === ":" || token === "/" ? (
          <Fragment key={i}>
            {token}
            <wbr />
          </Fragment>
        ) : (
          <Fragment key={i}>{token}</Fragment>
        ),
      )}
    </span>
  )
}

// ─── Shared link renderer ─────────────────────────────────────────────────────

function RouteLink({
  route,
  children,
  className,
  onClick,
  region: resourceRegion,
}: {
  route: ResolvedRoute
  children: React.ReactNode
  className?: string
  onClick?: MouseEventHandler
  /** The region the resource lives in, when it is known and may not be the console's. */
  region?: string
}) {
  // Always include a region so that middle-click / open-in-new-tab opens the
  // right one without relying on sessionStorage being copied: the resource's
  // own when it is known, the console's otherwise.
  const region = resourceRegion || endpointStore.get().region

  if (route.kind === "params")
    return (
      <Link
        from="/"
        to={route.to}
        params={route.params}
        search={{ ...route.search, region }}
        className={className}
        onClick={onClick}
      >
        {children}
      </Link>
    )
  return (
    <Link
      to={route.to}
      search={{ ...route.search, region }}
      className={className}
      onClick={onClick}
    >
      {children}
    </Link>
  )
}

// ─── ArnLink ─────────────────────────────────────────────────────────────────

interface ArnLinkProps {
  arn: string
  /** Display text. Defaults to the ARN itself. */
  label?: string
  /** Extra Tailwind classes merged onto the base font-mono/text-xs. */
  className?: string
}

/**
 * Renders an ARN as monospace text. Links to its detail page when the service
 * is recognised; plain text otherwise.
 */
export function ArnLink({ arn, label, className }: ArnLinkProps) {
  const base = cn("font-mono text-xs", className)
  const route = resolveArn(arn)
  const content = label != null ? <span>{label}</span> : <ArnText arn={arn} />
  if (!route) return <span className={base}>{content}</span>
  // An ARN names its region (IAM, S3 and CloudFront ones leave it empty), and
  // its page is in that region, whichever one the console is showing.
  return (
    <RouteLink route={route} className={cn(base, LINK_CLASS)} region={arn.split(":")[3]}>
      {content}
    </RouteLink>
  )
}

// ─── ResourceLink ─────────────────────────────────────────────────────────────

interface ResourceLinkProps {
  /**
   * Raw ARN — service and resource extracted automatically.
   * When provided, `service` and `resourceId` are ignored.
   */
  arn?: string
  /**
   * Short service name ("sqs") or AWS CloudFormation resource type
   * ("AWS::SQS::Queue"). Required when `arn` is not provided.
   */
  service?: string
  /**
   * Physical / logical resource identifier (queue name, table name, etc.).
   * Required when `arn` is not provided.
   */
  resourceId?: string
  /** Display text. Defaults to `arn ?? resourceId`. */
  label?: string
  className?: string
  onClick?: MouseEventHandler
  /** The region the resource is in, when it may not be the console's. Defaults to the ARN's, then the console's. */
  region?: string
}

/**
 * Links a resource to its detail page using either an ARN or a
 * service + resourceId pair. Renders plain text when the service is
 * unrecognised or neither arn nor resourceId is provided.
 */
export function ResourceLink({
  arn,
  service,
  resourceId,
  label,
  className,
  onClick,
  region,
}: ResourceLinkProps) {
  const display = label ?? arn ?? resourceId ?? ""
  const linked = cn(LINK_CLASS, className)

  // Prefer ARN resolution; fall back to service+id resolution
  const route =
    arn != null
      ? resolveArn(arn)
      : service != null && resourceId != null
        ? resolveService(service, resourceId)
        : null

  if (!route) return <span className={className}>{display}</span>
  return (
    <RouteLink
      route={route}
      className={linked}
      onClick={onClick}
      region={region ?? arn?.split(":")[3]}
    >
      {display}
    </RouteLink>
  )
}

// ─── LinkifiedText ─────────────────────────────────────────────────────────────

/**
 * Renders `text` verbatim, except any embedded `arn:...` substrings are
 * replaced with ArnLink (linked when the service is recognised, plain
 * monospace text otherwise). Everything else is rendered as a plain text
 * node — this only ever splits on a regex match, never
 * dangerouslySetInnerHTML, so it's safe to use on untrusted event payloads.
 *
 * Used by the Events page to auto-link ARNs that appear inside free-form
 * fields (error messages, log lines) rather than as a dedicated ARN field.
 */
export function LinkifiedText({ text, className }: { text: string; className?: string }) {
  if (!text.includes("arn:")) return <>{text}</>

  const matches = [...text.matchAll(EMBEDDED_ARN_PATTERN)]
  if (matches.length === 0) return <>{text}</>

  const nodes: ReactNode[] = []
  let cursor = 0
  matches.forEach((m, i) => {
    const start = m.index
    if (start > cursor) nodes.push(<Fragment key={`t${i}`}>{text.slice(cursor, start)}</Fragment>)
    nodes.push(<ArnLink key={`a${i}`} arn={m[0]} className={className} />)
    cursor = start + m[0].length
  })
  if (cursor < text.length) nodes.push(<Fragment key="tail">{text.slice(cursor)}</Fragment>)

  return <>{nodes}</>
}
