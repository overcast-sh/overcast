import { Sparkline } from "@/components/ui/sparkline"
import { formatQuantity } from "@/lib/format"
import { useEndpointTraffic } from "../use-endpoint-traffic"

const sum = (values: readonly number[] | undefined) => (values ?? []).reduce((a, b) => a + b, 0)

/** The last hour's requests as a sparkline, for a table cell. Empty while loading or when there was none. */
export function TrafficSpark({
  functionName,
  endpointName,
}: {
  functionName: string
  endpointName: string
}) {
  const { requests } = useEndpointTraffic(functionName, endpointName, { errors: false })
  const total = sum(requests)
  if (!requests || total === 0) return <span className="text-fg-subtle">—</span>
  return (
    <span
      className="inline-flex items-center gap-2 text-fg-muted"
      title={`${formatQuantity(total, "request")} in the last hour`}
    >
      <Sparkline data={requests} width={72} height={20} color="var(--color-accent)" />
      <span className="text-xs tabular-nums">{total}</span>
    </span>
  )
}

/** An endpoint's last hour: requests, client errors (4xx) and server errors (5xx), one bar per minute. */
export function TrafficPanel({
  functionName,
  endpointName,
}: {
  functionName: string
  endpointName: string
}) {
  const { requests, clientErrors, serverErrors } = useEndpointTraffic(functionName, endpointName)
  const rows = [
    { label: "Requests", data: requests, color: "var(--color-accent)" },
    { label: "4xx", data: clientErrors, color: "var(--color-warning)" },
    { label: "5xx", data: serverErrors, color: "var(--color-danger)" },
  ]
  return (
    <section aria-label="Traffic" className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">Traffic</h3>
      <p className="text-xs text-fg-muted">
        The last hour, per minute, from the <span className="font-mono">AWS/Lambda/Web</span>{" "}
        metrics. They are flushed in batches, so a request shows up within a minute.
      </p>
      <div className="grid grid-cols-[4rem_1fr_3rem] items-center gap-x-3 gap-y-1.5">
        {rows.map(({ label, data, color }) => (
          <TrafficRow key={label} label={label} data={data} color={color} />
        ))}
      </div>
    </section>
  )
}

function TrafficRow({
  label,
  data,
  color,
}: {
  label: string
  data: number[] | undefined
  color: string
}) {
  const total = sum(data)
  return (
    <>
      <span className="text-xs text-fg-subtle">{label}</span>
      {data && total > 0 ? (
        <Sparkline data={data} width={360} height={24} color={color} className="w-full" />
      ) : (
        <span className="text-xs text-fg-subtle">{data ? "none" : "…"}</span>
      )}
      <span className="text-right font-mono text-xs tabular-nums">{data ? total : ""}</span>
    </>
  )
}
