import { describe, expect, it, vi } from "vitest"
import { renderWithData, screen, within } from "@/test/render"
import {
  ecsClusterDetailQueryOptions,
  ecsTaskDefinitionFamiliesQueryOptions,
  ecsTaskDefinitionsQueryOptions,
  ecsTaskLogsQueryOptions,
  ecsServicesQueryOptions,
  ecsTasksQueryOptions,
} from "@/features/ecs/data"
import type { EcsCluster, EcsService, EcsTask, EcsTaskLogs } from "@/types"
import { ClusterDetail } from "./cluster-detail"
import { ServiceFailureAlert } from "./service-failure-alert"

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    params = {},
    search = {},
    onClick,
  }: {
    children: React.ReactNode
    to: string
    params?: Record<string, string>
    search?: Record<string, string>
    onClick?: React.MouseEventHandler<HTMLAnchorElement>
  }) => {
    const path = Object.entries(params).reduce(
      (result, [key, value]) => result.replace(`$${key}`, value),
      to,
    )
    const query = new URLSearchParams(search).toString()
    return (
      <a href={query ? `${path}?${query}` : path} onClick={onClick}>
        {children}
      </a>
    )
  },
}))

vi.mock("@/components/application-ownership-banner", () => ({
  ApplicationOwnershipBanner: () => null,
}))

describe("ClusterDetail tasks", () => {
  it("switches from current tasks to explicitly requested stopped tasks", async () => {
    const cluster: EcsCluster = {
      clusterName: "demo",
      clusterArn: "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
      status: "ACTIVE",
      runningTasksCount: 1,
      pendingTasksCount: 0,
      activeServicesCount: 1,
      registeredContainerInstancesCount: 0,
    }
    const running = {
      ...task("running-task", "RUNNING"),
      containers: [{ name: "app", lastStatus: "RUNNING", image: "example/app:latest" }],
    }
    const stopped = task("stopped-task", "STOPPED")

    const { user } = renderWithData(<ClusterDetail clusterName="demo" />, [
      [ecsClusterDetailQueryOptions("demo").queryKey, cluster],
      [ecsTasksQueryOptions("demo", "RUNNING").queryKey, [running]],
      [ecsTasksQueryOptions("demo", "STOPPED").queryKey, [stopped]],
      [ecsTaskDefinitionsQueryOptions().queryKey, []],
      [ecsTaskDefinitionFamiliesQueryOptions().queryKey, []],
    ])

    expect(screen.getByText("running-task")).toBeInTheDocument()
    expect(screen.queryByText("stopped-task")).not.toBeInTheDocument()

    await user.click(screen.getByText("running-task").closest("tr")!)
    expect(screen.getByRole("link", { name: "Open app logs" })).toHaveAttribute(
      "href",
      "/ecs/demo/tasks/running-task?container=app",
    )

    await user.click(screen.getByRole("radio", { name: /^Stopped/ }))

    expect(screen.getByText("stopped-task")).toBeInTheDocument()
    expect(screen.queryByText("running-task")).not.toBeInTheDocument()
  })

  it("orders tasks newest-first and reverses them on a header click", async () => {
    const cluster: EcsCluster = {
      clusterName: "demo",
      clusterArn: "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
      status: "ACTIVE",
      runningTasksCount: 2,
      pendingTasksCount: 0,
      activeServicesCount: 0,
      registeredContainerInstancesCount: 0,
    }
    const older = { ...task("older-task00", "RUNNING"), createdAt: "2026-08-01T10:00:00Z" }
    const newer = { ...task("newer-task00", "RUNNING"), createdAt: "2026-08-20T10:00:00Z" }

    const { user } = renderWithData(<ClusterDetail clusterName="demo" />, [
      [ecsClusterDetailQueryOptions("demo").queryKey, cluster],
      [ecsTasksQueryOptions("demo", "RUNNING").queryKey, [older, newer]],
      [ecsTasksQueryOptions("demo", "STOPPED").queryKey, []],
      [ecsTaskDefinitionsQueryOptions().queryKey, []],
      [ecsTaskDefinitionFamiliesQueryOptions().queryKey, []],
    ])

    const taskIds = () =>
      screen
        .getAllByRole("row")
        .slice(1)
        .map((row) => within(row).getAllByRole("cell")[0].textContent)

    expect(taskIds()).toEqual(["newer-task00", "older-task00"])

    await user.click(screen.getByRole("button", { name: /Created/ }))
    expect(taskIds()).toEqual(["older-task00", "newer-task00"])
  })

  it("opens a service selected by a topology deep link and connects it to its tasks", async () => {
    const cluster: EcsCluster = {
      clusterName: "demo",
      clusterArn: "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
      status: "ACTIVE",
      runningTasksCount: 1,
      pendingTasksCount: 0,
      activeServicesCount: 1,
      registeredContainerInstancesCount: 0,
    }

    const running = {
      ...task("website-task", "RUNNING"),
      group: "service:website",
    }
    const { user } = renderWithData(
      <ClusterDetail clusterName="demo" initialTab="services" initialService="website" />,
      [
        [ecsClusterDetailQueryOptions("demo").queryKey, cluster],
        [ecsServicesQueryOptions("demo").queryKey, [failedService()]],
        [ecsTasksQueryOptions("demo", "RUNNING").queryKey, [running]],
        [ecsTasksQueryOptions("demo", "STOPPED").queryKey, []],
        [ecsTaskDefinitionsQueryOptions().queryKey, []],
      ],
    )

    expect(screen.getByText("Events")).toBeInTheDocument()
    expect(screen.getByText(/unable to place a task/)).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "View active tasks" }))

    expect(screen.getByText("website-task")).toBeInTheDocument()
    expect(screen.getByText("website")).toBeInTheDocument()
  })
})

describe("ServiceFailureAlert", () => {
  it("surfaces retained container output ahead of a follow-on scheduler error", async () => {
    const failed = failedTask()
    const service = failedService()
    const logs: EcsTaskLogs = {
      taskArn: failed.taskArn,
      containerName: "app",
      logs: [
        "loading configuration",
        "entrypoint.sh: line 18: syntax error near unexpected token `fi'",
      ].join("\n"),
    }

    const parentClick = vi.fn()
    const { user } = renderWithData(
      <div onClick={parentClick}>
        <ServiceFailureAlert
          clusterName="demo"
          service={service}
          tasks={[failed]}
          onViewStoppedTasks={() => undefined}
        />
      </div>,
      [[ecsTaskLogsQueryOptions(failed.taskArn, "app").queryKey, logs]],
    )

    const alert = await screen.findByRole("alert")
    expect(within(alert).getByText("Likely cause")).toBeInTheDocument()
    expect(within(alert).getByText(/syntax error near unexpected token/)).toBeInTheDocument()
    expect(within(alert).getByText("Scheduler context")).toBeInTheDocument()
    const logsLink = within(alert).getByRole("link", { name: "Open task and logs" })
    expect(logsLink).toHaveAttribute("href", "/ecs/demo/tasks/failed-task?container=app")

    await user.click(logsLink)
    expect(parentClick).not.toHaveBeenCalled()
  })

  it("reports a recovered service without alarming on its later healthy task", async () => {
    // Given: a healthy service whose current task recovered from one recent failed start.
    const service = { ...failedService(), desiredCount: 1, runningCount: 1 }

    // When: service diagnostics are rendered.
    renderWithData(
      <ServiceFailureAlert
        clusterName="demo"
        service={service}
        tasks={[failedTask()]}
        onViewStoppedTasks={() => undefined}
      />,
      [],
    )

    // Then: the failure stays discoverable but the current healthy state leads the message.
    expect(await screen.findByRole("status")).toHaveTextContent(
      "Recovered after a recent task failure",
    )
  })
})

function task(id: string, status: string): EcsTask {
  return {
    taskArn: `arn:aws:ecs:us-east-1:000000000000:task/demo/${id}`,
    taskDefinitionArn: "arn:aws:ecs:us-east-1:000000000000:task-definition/web:1",
    clusterArn: "arn:aws:ecs:us-east-1:000000000000:cluster/demo",
    desiredStatus: status,
    lastStatus: status,
    launchType: "FARGATE",
    containers: [],
  }
}

function failedTask(): EcsTask {
  return {
    ...task("failed-task", "STOPPED"),
    group: "service:website",
    stoppedAt: new Date(Date.now() - 60_000).toISOString(),
    stoppedReason: "Essential container in task exited",
    stopCode: "EssentialContainerExited",
    containers: [{ name: "app", lastStatus: "STOPPED", exitCode: 2, reason: "exit 2" }],
  }
}

function failedService(): EcsService {
  return {
    serviceName: "website",
    serviceArn: "arn:aws:ecs:us-east-1:123456789012:service/demo/website",
    clusterArn: "arn:aws:ecs:us-east-1:123456789012:cluster/demo",
    taskDefinition: "app:2",
    desiredCount: 1,
    runningCount: 0,
    pendingCount: 0,
    status: "ACTIVE",
    launchType: "FARGATE",
    deployments: [],
    events: [
      {
        id: "event-1",
        createdAt: new Date().toISOString(),
        message:
          "(service website) was unable to place a task: container is marked for removal and cannot be connected to the network",
      },
    ],
    createdAt: new Date().toISOString(),
  }
}
