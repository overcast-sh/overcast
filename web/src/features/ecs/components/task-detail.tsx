import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronDown, ChevronRight, ScrollText } from "lucide-react"
import { ecsTaskLogsQueryOptions, ecsTaskQueryOptions } from "@/features/ecs/data"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { DockerBanner } from "@/components/docker-banner"
import { Badge } from "@/components/ui/badge"
import { CopyButton } from "@/components/ui/copy-button"
import { Button } from "@/components/ui/button"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { LogViewer } from "@/components/logs/log-viewer"
import type { EcsContainer } from "@/types"
import { DebugTargetPanel } from "@/features/debugger/components/debug-panel"

export function TaskDetail({
  clusterName,
  taskId,
  initialContainerName,
}: {
  clusterName: string
  taskId: string
  initialContainerName?: string
}) {
  const { data: task, isLoading } = useQuery(ecsTaskQueryOptions(clusterName, taskId))

  if (isLoading) {
    return (
      <div className="flex justify-center py-32">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!task) {
    return (
      <div className="flex justify-center py-32">
        <p className="text-sm text-fg-muted">Task not found</p>
      </div>
    )
  }

  const shortId = taskId.slice(0, 12)
  const serviceName = task.group?.startsWith("service:")
    ? task.group.slice("service:".length)
    : undefined

  return (
    <div className="flex w-full flex-col gap-6">
      <PageHeader
        title={shortId}
        description={
          <span className="flex items-center gap-2">
            <StatusBadge status={task.lastStatus} />
            <span className="text-fg-muted">{shortTaskDef(task.taskDefinitionArn)}</span>
          </span>
        }
      />

      <ApplicationOwnershipBanner candidates={[task.taskArn, task.taskDefinitionArn]} />

      <DockerBanner forService="ecs" />

      {/* Overview */}
      <section className="space-y-3">
        <h2 className="font-mono text-sm font-semibold text-fg">Overview</h2>
        <DefinitionList>
          <Definition label="Task ID" value={taskId} />
          <Definition label="Status" value={task.lastStatus} />
          <Definition label="Desired Status" value={task.desiredStatus} />
          <Definition label="Task Definition" value={shortTaskDef(task.taskDefinitionArn)} />
          <Definition label="Launch Type" value={task.launchType} />
          <Definition
            label="Started At"
            value={task.startedAt ? new Date(task.startedAt).toLocaleString() : null}
          />
          {task.group && (
            <Definition
              label="Group"
              value={
                serviceName ? (
                  <Link
                    to="/ecs/$cluster"
                    params={{ cluster: clusterName }}
                    search={{ tab: "services", service: serviceName }}
                    className="text-accent hover:underline"
                  >
                    {serviceName}
                  </Link>
                ) : (
                  task.group
                )
              }
            />
          )}
          {task.startedBy && <Definition label="Started By" value={task.startedBy} />}
          {task.stoppedAt && (
            <Definition label="Stopped At" value={new Date(task.stoppedAt).toLocaleString()} />
          )}
          {task.stopCode && <Definition label="Stop Code" value={task.stopCode} />}
          {task.stoppedReason && (
            <Definition label="Stop Reason" value={task.stoppedReason} variant="prose" />
          )}
        </DefinitionList>
      </section>

      {/* Containers */}
      <section className="space-y-3">
        <h2 className="font-mono text-sm font-semibold text-fg">Containers</h2>
        {task.containers.length === 0 ? (
          <p className="text-sm text-fg-muted">No containers</p>
        ) : (
          <div className="space-y-3">
            {task.containers.map((c) => (
              <ContainerCard
                key={c.name}
                taskId={taskId}
                taskArn={task.taskArn}
                container={c}
                initiallyShowLogs={
                  c.name === initialContainerName || (c.exitCode != null && c.exitCode !== 0)
                }
              />
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function ContainerCard({
  taskId,
  taskArn,
  container,
  initiallyShowLogs,
}: {
  taskId: string
  taskArn: string
  container: EcsContainer
  initiallyShowLogs: boolean
}) {
  const [envExpanded, setEnvExpanded] = useState(false)
  const [logsExpanded, setLogsExpanded] = useState(initiallyShowLogs)
  const logsQuery = useQuery(ecsTaskLogsQueryOptions(taskArn, container.name, logsExpanded))
  const logEvents = (logsQuery.data?.logs ?? "")
    .split(/\r?\n/)
    .filter((line) => line.length > 0)
    .map((message) => ({ message }))

  return (
    <div className="space-y-3 rounded border border-border bg-bg p-4">
      <div className="flex flex-wrap items-center gap-3">
        <span className="text-sm font-medium">{container.name}</span>
        <span className="text-xs text-fg-muted">{container.image ?? "—"}</span>
        <StatusBadge status={container.lastStatus} />
        {container.exitCode != null && (
          <Badge variant={container.exitCode === 0 ? "default" : "danger"}>
            exit: {container.exitCode}
          </Badge>
        )}
        <Button
          size="sm"
          variant="secondary"
          className="ml-auto"
          aria-expanded={logsExpanded}
          onClick={() => setLogsExpanded((expanded) => !expanded)}
        >
          <ScrollText className="h-3.5 w-3.5" aria-hidden="true" />
          {logsExpanded ? "Hide logs" : "View logs"}
        </Button>
      </div>

      {/* Why the container is in this state — set when it could not be started. */}
      {container.reason && <p className="text-sm text-danger">{container.reason}</p>}

      {logsExpanded && (
        <div className="space-y-1.5">
          <p className="font-mono text-xs font-medium text-fg-muted">Container output</p>
          <LogViewer
            events={logEvents}
            loading={logsQuery.isLoading}
            error={logsQuery.error instanceof Error ? logsQuery.error.message : null}
            emptyMessage="No container output was captured."
            showModeToggle={false}
            className="h-64"
          />
        </div>
      )}

      {/* Port mappings */}
      {container.networkBindings && container.networkBindings.length > 0 && (
        <div className="space-y-1">
          <p className="font-mono text-xs font-medium text-fg-muted">Port Mappings</p>
          <div className="flex flex-wrap gap-2">
            {container.networkBindings.map((b, i) => {
              const label = `${b.hostPort ?? "—"}:${b.containerPort ?? "—"}`
              return (
                <div
                  key={i}
                  className="flex items-center gap-1 rounded bg-bg-muted px-2 py-1 font-mono text-xs"
                >
                  <span>{label}</span>
                  {b.hostPort != null && (
                    <CopyButton value={String(b.hostPort)} noun="host port" tone="inline" />
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* Debugger — target id ecs/<taskId>/<container>; "off" until the task is tagged. */}
      <div className="space-y-1.5">
        <p className="font-mono text-xs font-medium text-fg-muted">Debugger</p>
        <DebugTargetPanel service="ecs" resource={taskId} container={container.name} />
      </div>

      {/* Environment variables — collapsible placeholder */}
      {/* ECS DescribeTasks doesn't return env vars directly, but we show the section if available */}
      <button
        className="flex items-center gap-1 text-xs text-fg-muted hover:text-fg"
        onClick={() => setEnvExpanded(!envExpanded)}
      >
        {envExpanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        Environment
      </button>
      {envExpanded && (
        <p className="pl-4 text-xs text-fg-muted">
          Environment variables are defined in the task definition.
        </p>
      )}
    </div>
  )
}

function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "RUNNING" || status === "ACTIVE"
      ? "success"
      : status === "PROVISIONING" || status === "PENDING"
        ? "warning"
        : status === "STOPPED" || status === "INACTIVE" || status === "DEPROVISIONING"
          ? "danger"
          : "default"
  return <Badge variant={variant}>{status}</Badge>
}

function shortTaskDef(arn: string) {
  const parts = arn.split("/")
  return parts.length > 1 ? parts[parts.length - 1] : arn
}
