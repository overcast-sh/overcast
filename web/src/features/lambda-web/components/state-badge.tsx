import { Badge, type BadgeProps } from "@/components/ui/badge"

const VARIANTS: Record<string, NonNullable<BadgeProps["variant"]>> = {
  Active: "success",
  Pending: "info",
  Deleting: "warning",
  Failed: "danger",
  InProgress: "info",
  Successful: "success",
}

/** A web function's, revision's or endpoint's state, or an endpoint's update status. */
export function StateBadge({ state }: { state: string | undefined }) {
  if (!state) return null
  return <Badge variant={VARIANTS[state] ?? "default"}>{state}</Badge>
}
