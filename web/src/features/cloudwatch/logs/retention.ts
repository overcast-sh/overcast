import { formatQuantity } from "@/lib/format"

/**
 * CloudWatch Logs retention periods.
 *
 * `retentionInDays` is not a free integer: AWS accepts only this fixed set and
 * answers anything else with `InvalidParameterException`, so the UI offers a
 * choice rather than a number field.
 * https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutRetentionPolicy.html
 */
export const LOG_RETENTION_DAYS = [
  1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288,
  3653,
] as const

/** Human label for a retention period, e.g. "1 day", "14 days". */
export function retentionLabel(days: number): string {
  return formatQuantity(days, "day")
}
