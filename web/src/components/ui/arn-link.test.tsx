/**
 * arn-link.test.tsx — covers the ARN → route resolution used by ArnLink and
 * the embedded-ARN linkification used by LinkifiedText, both consumed by the
 * Events page to auto-link resource ARNs (see internal/events.Event.ResourceARN
 * on the Go side and event-console.tsx's JsonString on the web side).
 *
 * Resolution itself lives in arn-routes.ts; these tests go through the
 * public component API and assert on rendered output — an anchor with the
 * expected href for a recognised service, and plain (non-link) text for an
 * unrecognised one.
 */
import type { FC } from "react"
import { describe, expect, it } from "vitest"
import { renderWithRouter, waitFor } from "@/test/render"
import { ArnLink, LinkifiedText, ResourceLink } from "./arn-link"

/**
 * `renderWithRouter` mounts a real TanStack Router, and the router's initial
 * load resolves asynchronously: on the synchronous tick after render
 * `router.state.status` is still `"pending"` and the container is empty.
 * Assertions here inspect the rendered markup as a whole rather than waiting
 * on one specific element, so wait for the router's first commit up front.
 */
async function renderRouted(component: FC, path: string) {
  const result = renderWithRouter(component, { path })
  await waitFor(() => expect(result.container).not.toBeEmptyDOMElement())
  return result
}

describe("ArnLink", () => {
  it("links a recognised SQS queue ARN to its detail page", async () => {
    const { container } = await renderRouted(
      () => <ArnLink arn="arn:aws:sqs:us-east-1:000000000000:my-queue" />,
      "/sqs/$queue",
    )
    const link = container.querySelector("a")
    expect(link).not.toBeNull()
    expect(link?.getAttribute("href")).toContain("/sqs/my-queue")
    expect(link?.textContent).toBe("arn:aws:sqs:us-east-1:000000000000:my-queue")
  })

  it("links a DynamoDB table ARN, ignoring a trailing GSI segment", async () => {
    const { container } = await renderRouted(
      () => <ArnLink arn="arn:aws:dynamodb:us-east-1:000000000000:table/orders/index/gsi1" />,
      "/dynamodb/$tableName",
    )
    const link = container.querySelector("a")
    expect(link?.getAttribute("href")).toContain("/dynamodb/orders")
  })

  it("links a Lambda function ARN to the function page, ignoring a version qualifier", async () => {
    const { container } = await renderRouted(
      () => <ArnLink arn="arn:aws:lambda:us-east-1:000000000000:function:my-fn:3" />,
      "/lambda/$name",
    )
    const link = container.querySelector("a")
    expect(link?.getAttribute("href")).toContain("/lambda/my-fn")
  })

  it.each([
    [
      "a state machine",
      "arn:aws:states:us-east-1:000000000000:stateMachine:orders:2",
      "/stepfunctions/orders",
    ],
    [
      "an execution",
      "arn:aws:states:us-east-1:000000000000:execution:orders:run-1",
      "/stepfunctions/execution/orders/run-1",
    ],
    ["an IAM role", "arn:aws:iam::000000000000:role/service/app-role", "/iam?tab=roles&q=app-role"],
    [
      "a log stream",
      "arn:aws:logs:us-east-1:000000000000:log-group:/aws/lambda/fn:log-stream:2026/09/22/abc",
      "/cloudwatch/logs/stream?groupName=%2Faws%2Flambda%2Ffn&streamName=2026%2F09%2F22%2Fabc",
    ],
    [
      "an Athena workgroup",
      "arn:aws:athena:us-east-1:000000000000:workgroup/etl",
      "/athena?tab=workgroups&q=etl",
    ],
    [
      "an Athena data catalog",
      "arn:aws:athena:us-east-1:000000000000:datacatalog/lake",
      "/athena?tab=data-catalogs&q=lake",
    ],
    ["a Glue database", "arn:aws:glue:us-east-1:000000000000:database/sales", "/glue/sales"],
    [
      "a Glue table",
      "arn:aws:glue:us-east-1:000000000000:table/sales/orders",
      "/glue/sales/orders",
    ],
    [
      "an S3 Tables bucket",
      "arn:aws:s3tables:us-east-1:000000000000:bucket/lake",
      "/s3tables/lake",
    ],
    [
      "an S3 Tables table",
      "arn:aws:s3tables:us-east-1:000000000000:bucket/lake/table/0f3c-9a",
      "/s3tables/lake/0f3c-9a",
    ],
  ])("links %s ARN to its page", async (_, arn, href) => {
    const { container } = await renderRouted(() => <ArnLink arn={arn} />, "/")
    expect(container.querySelector("a")?.getAttribute("href")).toContain(href)
  })

  it("renders plain text (no link) for a service with no mapped UI route", async () => {
    const { container } = await renderRouted(
      () => <ArnLink arn="arn:aws:acm:us-east-1:000000000000:certificate/abc-123" />,
      "/",
    )
    expect(container.querySelector("a")).toBeNull()
    expect(container.textContent).toBe("arn:aws:acm:us-east-1:000000000000:certificate/abc-123")
  })

  it("renders plain text for a non-ARN string", async () => {
    const { container } = await renderRouted(() => <ArnLink arn="not-an-arn" />, "/")
    expect(container.querySelector("a")).toBeNull()
    expect(container.textContent).toBe("not-an-arn")
  })
})

describe("ResourceLink", () => {
  // CloudFormation's resources list names each resource by type and physical id.
  it.each([
    ["AWS::Athena::WorkGroup", "etl", "/athena?tab=workgroups&q=etl"],
    ["AWS::Glue::Database", "sales", "/glue/sales"],
    [
      "AWS::S3Tables::TableBucket",
      "arn:aws:s3tables:us-east-1:000000000000:bucket/lake",
      "/s3tables/lake",
    ],
  ])("links a %s to its page", async (service, resourceId, href) => {
    const { container } = await renderRouted(
      () => <ResourceLink service={service} resourceId={resourceId} />,
      "/",
    )
    expect(container.querySelector("a")?.getAttribute("href")).toContain(href)
  })
})

describe("LinkifiedText", () => {
  it("linkifies an ARN embedded in a longer string, preserving surrounding text", async () => {
    const { container } = await renderRouted(
      () => (
        <LinkifiedText text="failed to invoke arn:aws:lambda:us-east-1:000000000000:function:my-fn: timeout" />
      ),
      "/lambda/$name",
    )
    expect(container.textContent).toBe(
      "failed to invoke arn:aws:lambda:us-east-1:000000000000:function:my-fn: timeout",
    )
    const link = container.querySelector("a")
    expect(link).not.toBeNull()
    expect(link?.getAttribute("href")).toContain("/lambda/my-fn")
  })

  it("renders text unchanged when no ARN is present", async () => {
    const { container } = await renderRouted(() => <LinkifiedText text="no arns here" />, "/")
    expect(container.querySelector("a")).toBeNull()
    expect(container.textContent).toBe("no arns here")
  })
})
