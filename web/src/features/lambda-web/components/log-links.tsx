import { Link } from "@tanstack/react-router"
import { systemLogGroup } from "../models"

/**
 * Links to a log group in the CloudWatch Logs console and to its `.local`
 * twin, where the system logs (environment starts and stops, one line per
 * request) go. Pre-selects the group.
 */
export function LogLinks({ logGroup }: { logGroup: string }) {
  const link = "text-accent hover:underline"
  return (
    <span className="flex flex-col gap-0.5">
      <span>
        <Link to="/cloudwatch/logs/group" search={{ groupName: logGroup }} className={link}>
          {logGroup}
        </Link>{" "}
        <span className="text-fg-subtle">application</span>
      </span>
      <span>
        <Link
          to="/cloudwatch/logs/group"
          search={{ groupName: systemLogGroup(logGroup) }}
          className={link}
        >
          {systemLogGroup(logGroup)}
        </Link>{" "}
        <span className="text-fg-subtle">system</span>
      </span>
    </span>
  )
}
