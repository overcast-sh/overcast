import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Play, Square, Plus, RefreshCw, Trash2, Pencil, ScrollText } from "lucide-react"
import {
  ecsClusterDetailQueryOptions,
  ecsTaskDefinitionsQueryOptions,
  ecsTasksQueryOptions,
  ecsServicesQueryOptions,
  ecsContainerInstancesQueryOptions,
  ecsKeys,
  ecsTagsQueryOptions,
  ecsTaskDefinitionFamiliesQueryOptions,
  runTaskMutationOptions,
  stopTaskMutationOptions,
  registerTaskDefinitionMutationOptions,
  deregisterTaskDefinitionMutationOptions,
  createServiceMutationOptions,
  updateServiceMutationOptions,
  deleteServiceMutationOptions,
} from "@/features/ecs/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { PageHeader, Spinner, EmptyState } from "@/components/ui/primitives"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import { ResourceTable } from "@/components/ui/resource-table"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { DockerBanner } from "@/components/docker-banner"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { Link } from "@tanstack/react-router"
import { AwsvpcFields } from "@/features/ecs/components/awsvpc-fields"
import { ServiceFailureAlert } from "@/features/ecs/components/service-failure-alert"
import { emptyAwsvpcNetworking, type AwsvpcNetworking } from "@/features/ecs/awsvpc"
import type { EcsTask, EcsTaskDefinition, EcsService, EcsContainerInstance } from "@/types"
import { fieldLabel } from "@/lib/typography"
import { formatPreciseTimeOfDay, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import {
  failedContainer,
  isSecondaryServiceFailureEvent,
  isServiceFailureEvent,
  isClusterTab,
  selectTasksForView,
  type ClusterTab,
  type TaskView,
} from "@/features/ecs/diagnostics"
import { SegmentedControl } from "@/components/ui/segmented-control"

export function ClusterDetail({
  clusterName,
  initialTab = "tasks",
  initialService,
}: {
  clusterName: string
  initialTab?: ClusterTab
  initialService?: string
}) {
  const [activeTab, setActiveTab] = useState(initialTab)
  const [taskView, setTaskView] = useState<TaskView>("auto")
  const [taskServiceFilter, setTaskServiceFilter] = useState<string>()

  const { data: cluster, isLoading } = useQuery(ecsClusterDetailQueryOptions(clusterName))

  if (isLoading) {
    return (
      <div className="flex justify-center py-32">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!cluster) return null

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title={cluster.clusterName}
        description={
          <span className="flex items-center gap-2">
            <TaskStatusBadge status={cluster.status} />
            <span className="text-fg-muted">
              {cluster.runningTasksCount} running · {cluster.activeServicesCount} services ·{" "}
              {cluster.pendingTasksCount} pending
            </span>
          </span>
        }
      />

      <ApplicationOwnershipBanner candidates={[cluster.clusterArn, cluster.clusterName]} />

      <DockerBanner forService="ecs" />

      <Tabs
        selectedKey={activeTab}
        onSelectionChange={(key) => {
          if (isClusterTab(key)) setActiveTab(key)
        }}
      >
        <TabList>
          <Tab id="tasks">Tasks</Tab>
          <Tab id="services">Services</Tab>
          <Tab id="container-instances">Container Instances</Tab>
          <Tab id="task-definitions">Task Definitions</Tab>
          <Tab id="tags">Tags</Tab>
        </TabList>

        <TabPanel id="tasks" className="pt-4">
          <TasksPanel
            clusterName={clusterName}
            view={taskView}
            serviceFilter={taskServiceFilter}
            onViewChange={setTaskView}
            onClearServiceFilter={() => setTaskServiceFilter(undefined)}
          />
        </TabPanel>

        <TabPanel id="services" className="pt-4">
          <ServicesPanel
            clusterName={clusterName}
            initialService={initialService}
            onViewTasks={(serviceName, view) => {
              setTaskView(view)
              setTaskServiceFilter(serviceName)
              setActiveTab("tasks")
            }}
          />
        </TabPanel>

        <TabPanel id="container-instances" className="pt-4">
          <ContainerInstancesPanel clusterName={clusterName} />
        </TabPanel>

        <TabPanel id="task-definitions" className="pt-4">
          <TaskDefinitionsPanel />
        </TabPanel>

        <TabPanel id="tags" className="pt-4">
          <ClusterTagsPanel clusterArn={cluster.clusterArn} />
        </TabPanel>
      </Tabs>
    </div>
  )
}

// ─── Tasks panel ──────────────────────────────────────────────────────────

function TasksPanel({
  clusterName,
  view,
  serviceFilter,
  onViewChange,
  onClearServiceFilter,
}: {
  clusterName: string
  view: TaskView
  serviceFilter?: string
  onViewChange: (view: TaskView) => void
  onClearServiceFilter: () => void
}) {
  const [showRunTask, setShowRunTask] = useState(false)
  const runningQuery = useQuery(ecsTasksQueryOptions(clusterName, "RUNNING"))
  const stoppedQuery = useQuery(ecsTasksQueryOptions(clusterName, "STOPPED"))
  const tasks = [...(runningQuery.data ?? []), ...(stoppedQuery.data ?? [])]
  const isLoading = runningQuery.isLoading || stoppedQuery.isLoading
  const isFetching = runningQuery.isFetching || stoppedQuery.isFetching
  const refetch = () => Promise.all([runningQuery.refetch(), stoppedQuery.refetch()])
  const { data: taskDefs = [] } = useQuery(ecsTaskDefinitionsQueryOptions())
  const selection = selectTasksForView(tasks, view, serviceFilter)

  const runMut = useResourceMutation({
    options: runTaskMutationOptions(),
    invalidateKeys: [ecsKeys.tasks(clusterName), ecsKeys.clusterDetail(clusterName)],
    successTitle: "Task started",
    onSuccess: () => setShowRunTask(false),
  })

  const stopMut = useResourceMutation({
    options: stopTaskMutationOptions(),
    invalidateKeys: [ecsKeys.tasks(clusterName), ecsKeys.clusterDetail(clusterName)],
    successTitle: "Task stopped",
    successVariant: "default",
  })

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")} />
          Refresh
        </Button>
        <Button size="sm" onClick={() => setShowRunTask(true)}>
          <Play className="mr-1.5 h-3.5 w-3.5" />
          Run Task
        </Button>
        <SegmentedControl
          label="Tasks"
          value={selection.effectiveView}
          options={[
            { value: "running", label: `Active ${selection.activeCount}` },
            { value: "stopped", label: `Stopped ${selection.stoppedCount}` },
          ]}
          onChange={onViewChange}
          className="ml-auto"
        />
      </div>

      {serviceFilter && (
        <div className="flex items-center gap-2 text-xs text-fg-muted">
          <span>
            Showing tasks for <span className="font-medium text-fg">{serviceFilter}</span>
          </span>
          <Button size="sm" variant="link" onClick={onClearServiceFilter}>
            Clear filter
          </Button>
        </div>
      )}

      {selection.isStoppedFallback && (
        <p className="rounded border border-border bg-bg-muted px-3 py-2 text-sm text-fg-muted">
          No tasks are running. Showing the most recently stopped tasks to help diagnose startup
          failures.
        </p>
      )}

      {selection.isTruncated && (
        <p className="text-xs text-fg-muted">
          Showing the 50 most recently stopped tasks out of {selection.stoppedCount}.
        </p>
      )}

      <ResourceTable
        variant="embedded"
        query={{ data: selection.tasks, isLoading }}
        noun="tasks"
        emptyTitle={`No ${selection.effectiveView} tasks`}
        emptyDescription={
          selection.effectiveView === "stopped"
            ? "No stopped tasks are retained for this view."
            : "No tasks are currently running."
        }
        rowKey={(t) => t.taskArn}
        defaultSort={{ id: "time", desc: true }}
        columns={[
          {
            header: "Task ID",
            sortValue: (t) => shortTaskId(t),
            cell: (t) => (
              <Link
                to="/ecs/$cluster/tasks/$taskId"
                params={{ cluster: clusterName, taskId: shortTaskId(t) }}
                className="text-accent hover:underline"
                onClick={(e) => e.stopPropagation()}
              >
                {shortTaskId(t).slice(0, 12)}
              </Link>
            ),
          },
          { header: "Status", cell: (t) => <TaskStatusBadge status={t.lastStatus} /> },
          {
            header: "Task Definition",
            cellClassName: "max-w-xs truncate text-fg-muted",
            cell: (t) => shortTaskDef(t.taskDefinitionArn),
          },
          { header: "Outcome", cell: (t) => taskOutcome(t) },
          { header: "Launch Type", cell: (t) => t.launchType ?? "—" },
          {
            // Explicit id: this header is "Created" or "Stopped" depending on
            // the view, and the sort key must not move with it.
            id: "time",
            header: selection.effectiveView === "stopped" ? "Stopped" : "Created",
            cellClassName: "text-fg-muted",
            sortValue: (t) => (selection.effectiveView === "stopped" ? t.stoppedAt : t.createdAt),
            cell: (t) => taskDisplayTime(t, selection.effectiveView),
          },
        ]}
        rowActions={(t) =>
          t.lastStatus !== "STOPPED" ? (
            <Button
              size="icon"
              variant="ghost"
              className="text-fg-muted hover:text-danger"
              onClick={() => stopMut.mutate({ cluster: clusterName, task: t.taskArn })}
            >
              <Square className="h-3.5 w-3.5" />
            </Button>
          ) : null
        }
        // The containers belong under the task they are in, not at the bottom of
        // the list — which is where they had to go until `ResourceTable` grew
        // row expansion (#1327).
        expandedContent={(t) => <ContainersList clusterName={clusterName} task={t} />}
      />

      <RunTaskDialog
        open={showRunTask}
        onClose={() => setShowRunTask(false)}
        isPending={runMut.isPending}
        taskDefs={taskDefs}
        onSubmit={(taskDef, count, launchType, networking) =>
          runMut.mutate({
            cluster: clusterName,
            taskDefinition: taskDef,
            count,
            launchType,
            subnets: networking.subnets,
            securityGroups: networking.securityGroups,
            assignPublicIp: networking.assignPublicIp,
          })
        }
      />
    </div>
  )
}

function ContainersList({ clusterName, task }: { clusterName: string; task: EcsTask }) {
  if (task.containers.length === 0) {
    return <p className="text-sm text-fg-muted">No containers</p>
  }
  const taskId = task.taskArn.split("/").at(-1) ?? task.taskArn
  return (
    <div className="space-y-2">
      <p className="font-mono text-xs font-medium text-fg-muted">Containers</p>
      {task.containers.map((c) => (
        <div
          key={c.name}
          className="flex items-center gap-3 rounded border border-border bg-bg p-2 text-sm"
        >
          <span className="font-medium">{c.name}</span>
          <span className="text-xs text-fg-muted">{c.image}</span>
          <TaskStatusBadge status={c.lastStatus} />
          {c.exitCode != null && (
            <Badge variant={c.exitCode === 0 ? "default" : "danger"}>exit: {c.exitCode}</Badge>
          )}
          <Button asChild size="sm" variant="secondary" className="ml-auto">
            <Link
              to="/ecs/$cluster/tasks/$taskId"
              params={{ cluster: clusterName, taskId }}
              search={{ container: c.name }}
              onClick={(event) => event.stopPropagation()}
            >
              <ScrollText className="h-3.5 w-3.5" aria-hidden="true" />
              Open {c.name} logs
            </Link>
          </Button>
        </div>
      ))}
    </div>
  )
}

// ─── Task Definitions panel ───────────────────────────────────────────────

function TaskDefinitionsPanel() {
  const [showRegister, setShowRegister] = useState(false)
  const [expandedFamily, setExpandedFamily] = useState<string>()

  const {
    data: taskDefs = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(ecsTaskDefinitionsQueryOptions())

  const { data: families = [] } = useQuery(ecsTaskDefinitionFamiliesQueryOptions())

  const registerMut = useResourceMutation({
    options: registerTaskDefinitionMutationOptions(),
    invalidateKeys: [ecsKeys.taskDefinitions()],
    successTitle: "Task definition registered",
    onSuccess: () => setShowRegister(false),
  })

  const deregisterMut = useResourceMutation({
    options: deregisterTaskDefinitionMutationOptions(),
    invalidateKeys: [ecsKeys.taskDefinitions()],
    successTitle: "Task definition deregistered",
    successVariant: "default",
  })

  // Group task definitions by family
  const byFamily = families.map((family) => ({
    family,
    revisions: taskDefs
      .filter((td) => td.family === family)
      .sort((a, b) => b.revision - a.revision),
  }))

  // Include any task defs whose family isn't in the families list
  const knownFamilies = new Set(families)
  const ungrouped = taskDefs.filter((td) => !knownFamilies.has(td.family))

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")} />
          Refresh
        </Button>
        <Button size="sm" onClick={() => setShowRegister(true)}>
          <Plus className="mr-1.5 h-3.5 w-3.5" />
          Register Task Definition
        </Button>
      </div>

      {isLoading ? (
        <div className="flex justify-center py-12">
          <Spinner className="h-6 w-6" />
        </div>
      ) : taskDefs.length === 0 ? (
        <EmptyState
          title="No task definitions"
          description="Register a task definition to define your containers."
          action={
            <Button size="sm" onClick={() => setShowRegister(true)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              Register Task Definition
            </Button>
          }
        />
      ) : (
        <div className="flex flex-col gap-2">
          {byFamily.map(({ family, revisions }) => (
            <div key={family} className="overflow-hidden rounded-md border border-border">
              <button
                className="flex w-full items-center justify-between px-4 py-2.5 text-left text-sm font-medium transition-colors hover:bg-accent-muted"
                onClick={() => setExpandedFamily(expandedFamily === family ? undefined : family)}
              >
                <span>{family}</span>
                <span className="flex items-center gap-2 text-xs text-fg-muted">
                  <Badge variant="default">{formatQuantity(revisions.length, "revision")}</Badge>
                  <span>{expandedFamily === family ? "▲" : "▼"}</span>
                </span>
              </button>

              {expandedFamily === family && (
                <ResourceTable
                  variant="embedded"
                  query={{ data: revisions, isLoading: false }}
                  noun="revisions"
                  emptyTitle="No revisions"
                  rowKey={(td) => td.taskDefinitionArn}
                  // The family list was already built newest-revision-first.
                  defaultSort={{ id: "revision", desc: true }}
                  columns={[
                    {
                      id: "revision",
                      header: "Revision",
                      sortValue: (td) => td.revision,
                      cell: (td) => td.revision,
                    },
                    { header: "Status", cell: (td) => <TaskStatusBadge status={td.status} /> },
                    { header: "CPU", cellClassName: "text-fg-muted", cell: (td) => td.cpu ?? "—" },
                    {
                      header: "Memory",
                      cellClassName: "text-fg-muted",
                      cell: (td) => td.memory ?? "—",
                    },
                  ]}
                  rowActions={(td) => (
                    <Button
                      size="icon"
                      variant="ghost"
                      className="text-fg-muted hover:text-danger"
                      onClick={() => deregisterMut.mutate(td.taskDefinitionArn)}
                    >
                      <Square className="h-3.5 w-3.5" />
                    </Button>
                  )}
                />
              )}
            </div>
          ))}

          {ungrouped.length > 0 && (
            <div className="overflow-hidden rounded-md border border-border">
              <button
                className="flex w-full items-center justify-between px-4 py-2.5 text-left text-sm font-medium transition-colors hover:bg-accent-muted"
                onClick={() =>
                  setExpandedFamily(
                    expandedFamily === "__ungrouped__" ? undefined : "__ungrouped__",
                  )
                }
              >
                <span className="text-fg-muted">Other</span>
                <span className="flex items-center gap-2 text-xs text-fg-muted">
                  <Badge variant="default">{ungrouped.length}</Badge>
                  <span>{expandedFamily === "__ungrouped__" ? "▲" : "▼"}</span>
                </span>
              </button>
              {expandedFamily === "__ungrouped__" && (
                <ResourceTable
                  variant="embedded"
                  query={{ data: ungrouped, isLoading: false }}
                  noun="task definitions"
                  emptyTitle="No task definitions"
                  rowKey={(td) => td.taskDefinitionArn}
                  columns={[
                    {
                      header: "Family",
                      cellClassName: "font-medium",
                      sortValue: (td) => td.family,
                      cell: (td) => td.family,
                    },
                    {
                      header: "Revision",
                      sortValue: (td) => td.revision,
                      cell: (td) => td.revision,
                    },
                    { header: "Status", cell: (td) => <TaskStatusBadge status={td.status} /> },
                  ]}
                  rowActions={(td) => (
                    <Button
                      size="icon"
                      variant="ghost"
                      className="text-fg-muted hover:text-danger"
                      onClick={() => deregisterMut.mutate(td.taskDefinitionArn)}
                    >
                      <Square className="h-3.5 w-3.5" />
                    </Button>
                  )}
                />
              )}
            </div>
          )}
        </div>
      )}

      <RegisterTaskDefDialog
        open={showRegister}
        onClose={() => setShowRegister(false)}
        isPending={registerMut.isPending}
        onSubmit={(json) => registerMut.mutate(json)}
      />
    </div>
  )
}

// ─── Services panel ───────────────────────────────────────────────────────

function ServicesPanel({
  clusterName,
  initialService,
  onViewTasks,
}: {
  clusterName: string
  initialService?: string
  onViewTasks: (serviceName: string, view: Exclude<TaskView, "auto">) => void
}) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()
  const [updateTarget, setUpdateTarget] = useState<EcsService>()

  const {
    data: services = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(ecsServicesQueryOptions(clusterName))
  const { data: taskDefs = [] } = useQuery(ecsTaskDefinitionsQueryOptions())
  const { data: tasks = [] } = useQuery(ecsTasksQueryOptions(clusterName, "STOPPED"))

  const createMut = useResourceMutation({
    options: createServiceMutationOptions(),
    invalidateKeys: [ecsKeys.services(clusterName), ecsKeys.clusterDetail(clusterName)],
    successTitle: "Service created",
    onSuccess: () => setShowCreate(false),
  })

  const updateMut = useResourceMutation({
    options: updateServiceMutationOptions(),
    invalidateKeys: [ecsKeys.services(clusterName), ecsKeys.clusterDetail(clusterName)],
    successTitle: "Service updated",
    onSuccess: () => setUpdateTarget(undefined),
  })

  const deleteMut = useResourceMutation({
    options: deleteServiceMutationOptions(),
    invalidateKeys: [ecsKeys.services(clusterName), ecsKeys.clusterDetail(clusterName)],
    successTitle: "Service deleted",
    successVariant: "default",
    onSuccess: () => setDeleteTarget(undefined),
  })

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <Button size="sm" variant="ghost" onClick={() => refetch()} disabled={isFetching}>
          <RefreshCw className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")} />
          Refresh
        </Button>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus className="mr-1.5 h-3.5 w-3.5" />
          Create Service
        </Button>
      </div>

      {/* Expanded service detail renders below the table — see the Tasks panel. */}
      <ResourceTable
        variant="embedded"
        query={{ data: services, isLoading }}
        noun="services"
        emptyTitle="No services"
        emptyDescription="Create a service to maintain running tasks."
        emptyAction={
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Create Service
          </Button>
        }
        rowKey={(svc) => svc.serviceArn}
        // A row opens its own detail under itself; `initialService` is the deep
        // link from the Tasks tab's "back to the service" affordance.
        expandedContent={(svc) => (
          <ServiceDetailPanel
            clusterName={clusterName}
            service={svc}
            tasks={tasks}
            onViewTasks={(view) => onViewTasks(svc.serviceName, view)}
          />
        )}
        defaultExpanded={(svc) => svc.serviceName === initialService}
        columns={[
          {
            header: "Service Name",
            cellClassName: "font-medium",
            sortValue: (svc) => svc.serviceName,
            cell: (svc) => svc.serviceName,
          },
          {
            header: "Status",
            cell: (svc) => {
              const primary = svc.deployments.find((d) => d.status === "PRIMARY")
              return (
                <div className="flex items-center gap-1.5">
                  <TaskStatusBadge status={svc.status} />
                  {primary?.rolloutState && <RolloutStateBadge state={primary.rolloutState} />}
                </div>
              )
            },
          },
          {
            header: "Running",
            sortValue: (svc) => svc.runningCount,
            cell: (svc) => (
              <>
                <span
                  className={cn(
                    svc.runningCount < svc.desiredCount ? "text-warning" : "text-success",
                  )}
                >
                  {svc.runningCount}/{svc.desiredCount} running
                </span>
                {svc.pendingCount > 0 && (
                  <span className="ml-1 text-xs text-fg-muted">({svc.pendingCount} pending)</span>
                )}
              </>
            ),
          },
          {
            header: "Task Definition",
            cellClassName: "max-w-xs truncate text-fg-muted",
            cell: (svc) => shortTaskDef(svc.taskDefinition),
          },
          { header: "Launch Type", cell: (svc) => svc.launchType || "—" },
          {
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (svc) => svc.createdAt,
            cell: (svc) => (svc.createdAt ? new Date(svc.createdAt).toLocaleString() : "—"),
          },
        ]}
        rowActions={(svc) => (
          <>
            <Button
              size="icon"
              variant="ghost"
              className="h-7 w-7 text-fg-muted"
              onClick={() => setUpdateTarget(svc)}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
            <Button
              size="icon"
              variant="ghost"
              className="h-7 w-7 text-fg-muted hover:text-danger"
              onClick={() => setDeleteTarget(svc.serviceName)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </>
        )}
      />

      <CreateServiceDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        isPending={createMut.isPending}
        taskDefs={taskDefs}
        onSubmit={(serviceName, taskDefinition, desiredCount, launchType, networking) =>
          createMut.mutate({
            cluster: clusterName,
            serviceName,
            taskDefinition,
            desiredCount,
            launchType,
            subnets: networking.subnets,
            securityGroups: networking.securityGroups,
            assignPublicIp: networking.assignPublicIp,
          })
        }
      />

      <Dialog
        open={!!deleteTarget}
        onOpenChange={(v) => {
          if (!v) setDeleteTarget(undefined)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Service</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <p className="text-sm text-fg-muted">
              Are you sure you want to delete service{" "}
              <span className="font-medium text-fg">{deleteTarget}</span>?
            </p>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleteTarget(undefined)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              disabled={deleteMut.isPending}
              onClick={() =>
                deleteTarget && deleteMut.mutate({ cluster: clusterName, service: deleteTarget })
              }
            >
              {deleteMut.isPending && <Spinner className="mr-2" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <UpdateServiceDialog
        open={!!updateTarget}
        onClose={() => setUpdateTarget(undefined)}
        isPending={updateMut.isPending}
        service={updateTarget}
        onSubmit={(desiredCount) =>
          updateTarget &&
          updateMut.mutate({
            cluster: clusterName,
            service: updateTarget.serviceName,
            desiredCount,
          })
        }
      />
    </div>
  )
}

// ─── Service detail ───────────────────────────────────────────────────────

/**
 * Deployment health plus a compact event history. Recent stopped-task
 * diagnostics lead this view; scheduler events remain available as context.
 */
function ServiceDetailPanel({
  clusterName,
  service,
  tasks,
  onViewTasks,
}: {
  clusterName: string
  service: EcsService
  tasks: EcsTask[]
  onViewTasks: (view: Exclude<TaskView, "auto">) => void
}) {
  const primary = service.deployments.find((d) => d.status === "PRIMARY")
  const [showAllEvents, setShowAllEvents] = useState(false)
  const visibleEvents = showAllEvents ? service.events : service.events.slice(0, 5)

  return (
    <div className="flex flex-col gap-4">
      <ServiceFailureAlert
        clusterName={clusterName}
        service={service}
        tasks={tasks}
        onViewStoppedTasks={() => onViewTasks("stopped")}
      />
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" onClick={() => onViewTasks("running")}>
          View active tasks
        </Button>
        <Button size="sm" variant="ghost" onClick={() => onViewTasks("stopped")}>
          View stopped tasks
        </Button>
      </div>
      {primary && (
        <div>
          <h4 className={cn(fieldLabel, "mb-1.5 text-fg")}>Primary deployment</h4>
          {/* Stacked, because four short counts read as a row of stats rather
              than a run of label/value pairs. */}
          <DefinitionList columns={4} layout="stacked" className="gap-y-1">
            <Definition
              label="Rollout"
              value={
                primary.rolloutState ? (
                  <RolloutStateBadge state={primary.rolloutState} />
                ) : undefined
              }
            />
            <Definition label="Running" value={`${primary.runningCount}/${primary.desiredCount}`} />
            <Definition label="Pending" value={primary.pendingCount} />
            <Definition
              label="Failed tasks"
              value={primary.failedTasks ?? 0}
              valueClassName={cn(primary.failedTasks && "text-danger")}
            />
          </DefinitionList>
          {primary.rolloutStateReason && (
            <p className="mt-1.5 text-xs text-fg-muted">{primary.rolloutStateReason}</p>
          )}
        </div>
      )}

      <div>
        <h4 className={cn(fieldLabel, "mb-1.5 text-fg")}>Events</h4>
        {service.events.length === 0 ? (
          <p className="text-sm text-fg-muted">No events yet.</p>
        ) : (
          <ul className="flex flex-col gap-1">
            {visibleEvents.map((e) => (
              <li key={e.id} className="flex gap-3 text-sm">
                <span className="shrink-0 text-xs text-fg-muted tabular-nums">
                  {e.createdAt ? (
                    <time dateTime={e.createdAt} title={new Date(e.createdAt).toLocaleString()}>
                      {formatPreciseTimeOfDay(e.createdAt)}
                    </time>
                  ) : (
                    "—"
                  )}
                </span>
                <span
                  className={cn(
                    isSecondaryServiceFailureEvent(e.message)
                      ? "text-fg-muted"
                      : isServiceFailureEvent(e.message)
                        ? "text-danger"
                        : "text-fg",
                  )}
                >
                  {e.message}
                </span>
              </li>
            ))}
          </ul>
        )}
        {service.events.length > 5 && (
          <Button
            size="sm"
            variant="link"
            className="mt-1"
            onClick={() => setShowAllEvents((shown) => !shown)}
          >
            {showAllEvents ? "Show recent events" : `Show all ${service.events.length} events`}
          </Button>
        )}
      </div>
    </div>
  )
}

function taskOutcome(task: EcsTask) {
  const container = failedContainer(task)
  if (container?.exitCode != null) {
    return (
      <Badge variant={container.exitCode === 0 ? "default" : "danger"}>
        exit: {container.exitCode}
      </Badge>
    )
  }
  if (task.stopCode) return <span className="text-xs text-fg-muted">{task.stopCode}</span>
  return <span className="text-fg-muted">{"\u2014"}</span>
}

function taskDisplayTime(task: EcsTask, view: "running" | "stopped") {
  const timestamp = view === "stopped" ? task.stoppedAt : task.createdAt
  return timestamp ? new Date(timestamp).toLocaleString() : "\u2014"
}

function RolloutStateBadge({ state }: { state: string }) {
  const variant = state === "COMPLETED" ? "success" : state === "FAILED" ? "danger" : "warning"
  return <Badge variant={variant}>{state}</Badge>
}

// ─── Shared helpers ───────────────────────────────────────────────────────

function TaskStatusBadge({ status }: { status: string }) {
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

/** The id segment of a task ARN — what the Task ID column shows and links to. */
function shortTaskId(task: EcsTask) {
  return task.taskArn.split("/").pop() ?? task.taskArn
}

function shortTaskDef(arn: string) {
  // arn:aws:ecs:...:task-definition/family:revision → family:revision
  const parts = arn.split("/")
  return parts.length > 1 ? parts[parts.length - 1] : arn
}

// ─── Run Task dialog ──────────────────────────────────────────────────────

function RunTaskDialog({
  open,
  onClose,
  isPending,
  taskDefs,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  taskDefs: EcsTaskDefinition[]
  onSubmit: (
    taskDef: string,
    count: number,
    launchType: string,
    networking: AwsvpcNetworking,
  ) => void
}) {
  const [taskDef, setTaskDef] = useState("")
  const [count, setCount] = useState(1)
  const [launchType, setLaunchType] = useState("FARGATE")
  const [networking, setNetworking] = useState<AwsvpcNetworking>(emptyAwsvpcNetworking)

  // A Fargate task definition always uses awsvpc networking, so ECS rejects the
  // call without a subnet. An EC2 launch may still be awsvpc, so the fields
  // stay available rather than being hidden.
  const selected = taskDefs.find((td) => td.taskDefinitionArn === taskDef)
  const requiresNetworking = launchType === "FARGATE" || selected?.networkMode === "awsvpc"
  const canRun = !!taskDef && (!requiresNetworking || networking.subnets.length > 0)

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Run Task</DialogTitle>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Task Definition</label>
            <select
              value={taskDef}
              onChange={(e) => setTaskDef(e.target.value)}
              className="w-full rounded border border-border bg-bg px-3 py-2 text-sm text-fg"
            >
              <option value="">Select a task definition</option>
              {taskDefs.map((td) => (
                <option key={td.taskDefinitionArn} value={td.taskDefinitionArn}>
                  {td.family}:{td.revision}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Count</label>
            <Input
              type="number"
              min={1}
              max={10}
              value={count}
              onChange={(e) => setCount(parseInt(e.target.value) || 1)}
            />
          </div>
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Launch Type</label>
            <select
              value={launchType}
              onChange={(e) => setLaunchType(e.target.value)}
              className="w-full rounded border border-border bg-bg px-3 py-2 text-sm text-fg"
            >
              <option value="FARGATE">FARGATE</option>
              <option value="EC2">EC2</option>
            </select>
          </div>
          <AwsvpcFields value={networking} onChange={setNetworking} required={requiresNetworking} />
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={isPending || !canRun}
            onClick={() => onSubmit(taskDef, count, launchType, networking)}
          >
            {isPending && <Spinner className="mr-2" />}
            Run
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Register Task Definition dialog ──────────────────────────────────────

const EXAMPLE_TASK_DEF = JSON.stringify(
  {
    family: "my-task",
    containerDefinitions: [
      {
        name: "app",
        image: "nginx:latest",
        essential: true,
        portMappings: [{ containerPort: 80, hostPort: 80, protocol: "tcp" }],
        memory: 512,
        cpu: 256,
      },
    ],
    cpu: "256",
    memory: "512",
  },
  null,
  2,
)

function RegisterTaskDefDialog({
  open,
  onClose,
  isPending,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  onSubmit: (json: string) => void
}) {
  const [json, setJson] = useState(EXAMPLE_TASK_DEF)

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose()
      }}
    >
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>Register Task Definition</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <Textarea
            className="min-h-64 font-mono text-xs"
            value={json}
            onChange={(e) => setJson(e.target.value)}
          />
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={isPending} onClick={() => onSubmit(json)}>
            {isPending && <Spinner className="mr-2" />}
            Register
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Create Service dialog ────────────────────────────────────────────────

function CreateServiceDialog({
  open,
  onClose,
  isPending,
  taskDefs,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  taskDefs: EcsTaskDefinition[]
  onSubmit: (
    serviceName: string,
    taskDefinition: string,
    desiredCount: number,
    launchType: string,
    networking: AwsvpcNetworking,
  ) => void
}) {
  const [serviceName, setServiceName] = useState("")
  const [taskDef, setTaskDef] = useState("")
  const [desiredCount, setDesiredCount] = useState(1)
  const [launchType, setLaunchType] = useState("FARGATE")
  const [networking, setNetworking] = useState<AwsvpcNetworking>(emptyAwsvpcNetworking)

  const selected = taskDefs.find((td) => td.taskDefinitionArn === taskDef)
  const requiresNetworking = launchType === "FARGATE" || selected?.networkMode === "awsvpc"
  const canCreate =
    !!serviceName && !!taskDef && (!requiresNetworking || networking.subnets.length > 0)

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create Service</DialogTitle>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Service Name</label>
            <Input
              value={serviceName}
              onChange={(e) => setServiceName(e.target.value)}
              placeholder="my-service"
            />
          </div>
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Task Definition</label>
            <select
              value={taskDef}
              onChange={(e) => setTaskDef(e.target.value)}
              className="w-full rounded border border-border bg-bg px-3 py-2 text-sm text-fg"
            >
              <option value="">Select a task definition</option>
              {taskDefs.map((td) => (
                <option key={td.taskDefinitionArn} value={td.taskDefinitionArn}>
                  {td.family}:{td.revision}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Desired Count</label>
            <Input
              type="number"
              min={0}
              max={100}
              value={desiredCount}
              onChange={(e) => setDesiredCount(parseInt(e.target.value) || 0)}
            />
          </div>
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Launch Type</label>
            <select
              value={launchType}
              onChange={(e) => setLaunchType(e.target.value)}
              className="w-full rounded border border-border bg-bg px-3 py-2 text-sm text-fg"
            >
              <option value="FARGATE">FARGATE</option>
              <option value="EC2">EC2</option>
            </select>
          </div>
          <AwsvpcFields value={networking} onChange={setNetworking} required={requiresNetworking} />
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={isPending || !canCreate}
            onClick={() => onSubmit(serviceName, taskDef, desiredCount, launchType, networking)}
          >
            {isPending && <Spinner className="mr-2" />}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Update Service dialog ────────────────────────────────────────────────

function UpdateServiceDialog({
  open,
  onClose,
  isPending,
  service,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  service?: EcsService
  onSubmit: (desiredCount: number) => void
}) {
  const [desiredCount, setDesiredCount] = useState(service?.desiredCount ?? 1)

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Update Service — {service?.serviceName}</DialogTitle>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>Desired Count</label>
            <Input
              type="number"
              min={0}
              max={100}
              value={desiredCount}
              onChange={(e) => setDesiredCount(parseInt(e.target.value) || 0)}
            />
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={isPending} onClick={() => onSubmit(desiredCount)}>
            {isPending && <Spinner className="mr-2" />}
            Update
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Container Instances Panel ────────────────────────────────────────────

function ContainerInstancesPanel({ clusterName }: { clusterName: string }) {
  const { data: instances = [], isLoading } = useQuery(
    ecsContainerInstancesQueryOptions(clusterName),
  )

  return (
    <ResourceTable
      variant="embedded"
      query={{ data: instances, isLoading }}
      noun="container instances"
      emptyTitle="No container instances"
      emptyDescription="No EC2 container instances are registered to this cluster."
      rowKey={(ci) => ci.containerInstanceArn}
      columns={[
        {
          header: "Container Instance ARN",
          sortValue: (ci) => shortContainerInstanceId(ci),
          cell: (ci) => (
            <span title={ci.containerInstanceArn}>
              {shortContainerInstanceId(ci).slice(0, 12)}…
            </span>
          ),
        },
        {
          header: "EC2 Instance",
          cellClassName: "text-fg-muted",
          cell: (ci) => ci.ec2InstanceId ?? "—",
        },
        { header: "Status", cell: (ci) => <TaskStatusBadge status={ci.status} /> },
        {
          header: "Agent Connected",
          cell: (ci) => (
            <Badge variant={ci.agentConnected ? "success" : "warning"}>
              {ci.agentConnected ? "Connected" : "Disconnected"}
            </Badge>
          ),
        },
        {
          header: "Running",
          sortValue: (ci) => ci.runningTasksCount,
          cell: (ci) => ci.runningTasksCount,
        },
        {
          header: "Pending",
          sortValue: (ci) => ci.pendingTasksCount,
          cell: (ci) => ci.pendingTasksCount,
        },
        {
          header: "Registered At",
          cellClassName: "text-fg-muted",
          sortValue: (ci) => ci.registeredAt,
          cell: (ci) => (ci.registeredAt ? new Date(ci.registeredAt).toLocaleString() : "—"),
        },
      ]}
    />
  )
}

/** The id segment of a container-instance ARN — what the first column shows. */
function shortContainerInstanceId(ci: EcsContainerInstance) {
  return ci.containerInstanceArn.split("/").pop() ?? ci.containerInstanceArn
}

// ─── Cluster Tags Panel ───────────────────────────────────────────────────

function ClusterTagsPanel({ clusterArn }: { clusterArn: string }) {
  const { data: tags = [], isLoading } = useQuery(ecsTagsQueryOptions(clusterArn))

  return (
    <ResourceTable
      variant="embedded"
      query={{ data: tags, isLoading }}
      noun="tags"
      emptyTitle="No tags"
      emptyDescription="This cluster has no tags."
      rowKey={(tag) => tag.key}
      columns={[
        { header: "Key", sortValue: (tag) => tag.key, cell: (tag) => tag.key },
        { header: "Value", cellClassName: "text-fg-muted", cell: (tag) => tag.value },
      ]}
    />
  )
}
