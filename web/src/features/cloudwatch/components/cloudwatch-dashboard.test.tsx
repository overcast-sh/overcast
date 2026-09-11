/**
 * The CloudWatch Metrics page's browser and detail pane: namespace grouping,
 * the metric filter, the dimension chips that replaced the one-line dimension
 * string, and the selected metric's summary and datapoints.
 */
import type { Metric } from "@aws-sdk/client-cloudwatch"
import { describe, expect, it } from "vitest"
import { createTestQueryClient, renderWithRouter, screen, within } from "@/test/render"
import { ToastContextProvider } from "@/components/ui/toast"
import { TooltipProvider } from "@/components/ui/tooltip"
import {
  cloudwatchAlarmsQueryOptions,
  cloudwatchMetricsQueryOptions,
  cloudwatchMetricStatisticsQueryOptions,
} from "@/features/cloudwatch/metrics/data"
import { Route as CloudwatchLogsIndexRoute } from "@/routes/cloudwatch/logs/index"
import { CloudwatchDashboard } from "./cloudwatch-dashboard"

describe("CloudWatch Logs route metadata", () => {
  it("defines the CloudWatch Logs page title", async () => {
    const head = await CloudwatchLogsIndexRoute.options.head?.({} as never)
    expect(head?.meta?.[0]?.title).toBe("CloudWatch Logs — Overcast")
  })
})

const LONG_ARN =
  "arn:aws:lambda:us-east-1:000000000000:function:checkout-service-orders-processor-production"

const METRICS: Metric[] = [
  {
    Namespace: "AWS/Lambda",
    MetricName: "Invocations",
    Dimensions: [{ Name: "FunctionName", Value: LONG_ARN }],
  },
  { Namespace: "AWS/SQS", MetricName: "NumberOfMessagesSent", Dimensions: [] },
  {
    Namespace: "AWS/Lambda",
    MetricName: "Errors",
    Dimensions: [{ Name: "FunctionName", Value: "checkout" }],
  },
]

// Inside the default "last 1 hour" window, which the chart clips to.
const T0 = Date.now()
const at = (minutesAgo: number) => new Date(T0 - minutesAgo * 60_000)

function renderDashboard() {
  const queryClient = createTestQueryClient()
  queryClient.setQueryData(cloudwatchMetricsQueryOptions().queryKey, METRICS)
  queryClient.setQueryData(cloudwatchAlarmsQueryOptions().queryKey, [])
  // The first metric in publish order is the default selection, read as
  // Average over the last hour at 60-second periods.
  queryClient.setQueryData(
    cloudwatchMetricStatisticsQueryOptions({
      namespace: "AWS/Lambda",
      metricName: "Invocations",
      dimensions: METRICS[0].Dimensions ?? [],
      stat: "Average",
      period: 60,
      rangeHours: 1,
    }).queryKey,
    {
      datapoints: [
        { Timestamp: at(3), Average: 2, SampleCount: 2, Unit: "Count" },
        { Timestamp: at(2), Average: 8, SampleCount: 4, Unit: "Count" },
        { Timestamp: at(1), Average: 5, SampleCount: 5, Unit: "Count" },
      ],
    },
  )
  return renderWithRouter(
    () => (
      <ToastContextProvider>
        <TooltipProvider>
          <CloudwatchDashboard />
        </TooltipProvider>
      </ToastContextProvider>
    ),
    { queryClient },
  )
}

describe("CloudwatchDashboard > metric browser", () => {
  it("groups metrics by namespace, sorted, with a count per group", async () => {
    renderDashboard()

    const lambda = await screen.findByTitle("AWS/Lambda")
    const sqs = screen.getByTitle("AWS/SQS")
    expect(lambda.compareDocumentPosition(sqs) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(lambda.parentElement!).getByText("2")).toBeInTheDocument()
    expect(within(sqs.parentElement!).getByText("1")).toBeInTheDocument()
  })

  it("renders each dimension as its own chip, so a long value truncates by itself", async () => {
    renderDashboard()

    const chips = await screen.findAllByTitle(`FunctionName=${LONG_ARN}`)
    // One in the browser row and one in the detail pane's header.
    expect(chips).toHaveLength(2)
    for (const chip of chips) {
      expect(chip.textContent).toBe(`FunctionName=${LONG_ARN}`)
      expect(chip.querySelector(".truncate")?.textContent).toBe(LONG_ARN)
    }
  })

  it("filters by name, namespace or dimension and says how many remain", async () => {
    const { user } = renderDashboard()
    await screen.findByTitle("AWS/SQS")

    await user.type(screen.getByRole("textbox", { name: /filter metrics/i }), "checkout")

    expect(screen.getByText("2 of 3")).toBeInTheDocument()
    expect(screen.queryByTitle("AWS/SQS")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /^Errors/ })).toBeInTheDocument()

    await user.clear(screen.getByRole("textbox", { name: /filter metrics/i }))
    await user.type(screen.getByRole("textbox", { name: /filter metrics/i }), "nothing here")

    expect(screen.getByText("No metrics match the filter.")).toBeInTheDocument()
  })
})

describe("CloudwatchDashboard > selected metric", () => {
  it("selects the first published metric and marks it pressed", async () => {
    renderDashboard()

    const selected = await screen.findByRole("button", { name: /^Invocations/, pressed: true })
    expect(selected).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: "Invocations" })).toBeInTheDocument()
  })

  it("summarises the range — latest, mean, min, max — in the metric's unit", async () => {
    renderDashboard()

    // Latest and the mean of 2, 8, 5 are both 5; max 8; min 2.
    expect(await screen.findAllByTitle("5 Count")).toHaveLength(2)
    expect(screen.getByTitle("8 Count")).toBeInTheDocument()
    expect(screen.getByTitle("2 Count")).toBeInTheDocument()
    expect(screen.getByText("Mean")).toBeInTheDocument()
  })

  it("lists the datapoints newest first", async () => {
    renderDashboard()

    const table = (await screen.findByRole("table")) as HTMLTableElement
    const values = [...table.tBodies[0].rows].map((row) => row.cells[1].textContent)
    expect(values).toEqual(["5", "8", "2"])
    expect(screen.getByText("3 returned · newest first")).toBeInTheDocument()
  })

  it("draws the metric with the Monitor tab's chart", async () => {
    renderDashboard()

    expect(await screen.findByRole("img", { name: "Average over time" })).toBeInTheDocument()
  })
})
