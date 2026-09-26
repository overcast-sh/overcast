/**
 * node-route — where a map node (or a row inside one) navigates to, and the
 * click handlers every node shares: a click opens the page in place, switching
 * region first when the resource lives in another one, and a middle click
 * opens it in a new tab.
 */

import { useCallback } from "react"
import { useNavigate } from "@tanstack/react-router"
import { ATHENA_TAB } from "@/components/ui/arn-routes"
import { useEndpoint } from "@/hooks/use-endpoint"
import { endpointStore } from "@/services/endpoint-store"
import type { FileRoutesByTo } from "@/routeTree.gen"
import type { TopologyECSResourceType, TopologyGlueResourceType } from "@/types"

export interface NodeRoute {
  to: keyof FileRoutesByTo
  params?: Record<string, string>
  search?: Record<string, string>
}

export function routeHref(route: NodeRoute, search?: Record<string, string | undefined>): string {
  let href = route.to as string
  for (const [key, value] of Object.entries(route.params ?? {})) {
    href = href.replace(`$${key}`, encodeURIComponent(value))
  }
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries({ ...(route.search ?? {}), ...(search ?? {}) })) {
    if (value) params.set(key, value)
  }
  const query = params.toString()
  return query ? `${href}?${query}` : href
}

export function openRouteInNewTab(route: NodeRoute, search?: Record<string, string | undefined>) {
  window.open(routeHref(route, search), "_blank", "noopener,noreferrer")
}

export interface NodeRouteInput {
  service: string
  label: string
  nodeId?: string
  protocolType?: string
  ecsResourceType?: TopologyECSResourceType
  clusterName?: string
  taskId?: string
  scope?: string
  glueResourceType?: TopologyGlueResourceType
}

/**
 * Returns the deepest available route for a given service+resource name,
 * or null if there is no per-resource page.
 */
export function nodeRoute({
  service,
  label,
  nodeId,
  protocolType,
  ecsResourceType,
  clusterName,
  taskId,
  scope,
  glueResourceType,
}: NodeRouteInput): NodeRoute | null {
  switch (service) {
    case "s3":
      return { to: "/s3/$bucket", params: { bucket: label } }
    case "sqs":
      return { to: "/sqs/$queue", params: { queue: label } }
    case "dynamodb":
      return { to: "/dynamodb/$tableName", params: { tableName: label } }
    case "sns":
      return { to: "/sns/$topic", params: { topic: label } }
    case "lambda":
      return { to: "/lambda/$name", params: { name: label } }
    case "logs":
      return { to: "/cloudwatch/logs/group" as const, search: { groupName: label } }
    case "ecs":
      if (ecsResourceType === "task" && clusterName && taskId) {
        return {
          to: "/ecs/$cluster/tasks/$taskId",
          params: { cluster: clusterName, taskId },
        }
      }
      if (ecsResourceType === "service" && clusterName) {
        return {
          to: "/ecs/$cluster",
          params: { cluster: clusterName },
          search: { tab: "services", service: label },
        }
      }
      return { to: "/ecs/$cluster", params: { cluster: clusterName ?? label } }
    case "ecr":
      return { to: "/ecr/$repositoryName", params: { repositoryName: label } }
    case "waf": {
      const webAclId = nodeId?.split("::")[2]
      return webAclId && scope
        ? {
            to: "/waf/$scope/$webAclId/$name",
            params: { scope, webAclId, name: label },
          }
        : { to: "/waf" }
    }
    case "ec2":
      return { to: "/ec2/$instanceId", params: { instanceId: label } }
    case "rds":
      return { to: "/rds/$instance", params: { instance: label } }
    case "apigateway": {
      // Node ID format: "region::apigateway::apiId" — extract the API ID.
      const apiId = nodeId?.split("::")[2]
      if (!apiId) return { to: "/apigateway" }
      if (protocolType === "REST") {
        return { to: "/apigateway/rest/$apiId", params: { apiId } }
      }
      return { to: "/apigateway/http/$apiId", params: { apiId } }
    }
    case "appsync": {
      // Node ID format: "region::appsync::apiId" — extract the API ID.
      const apiId = nodeId?.split("::")[2]
      return apiId ? { to: "/appsync/$apiId", params: { apiId } } : { to: "/appsync" }
    }
    case "cognito": {
      // Node ID format: "region::cognito::poolId" — extract the pool ID.
      const poolId = nodeId?.split("::")[2]
      return poolId ? { to: "/cognito/$poolId", params: { poolId } } : { to: "/cognito" }
    }
    case "msk":
      return { to: "/msk" }
    case "athena":
      return { to: "/athena", search: { tab: ATHENA_TAB.workgroups, workgroup: label } }
    case "glue":
      return glueResourceType === "catalog"
        ? { to: "/glue" }
        : { to: "/glue/$database", params: { database: label } }
    case "s3tables":
      return { to: "/s3tables/$bucket", params: { bucket: label } }
    default:
      return null
  }
}

/** A Glue table's page. */
export function glueTableRoute(database: string, table: string): NodeRoute {
  return { to: "/glue/$database/$table", params: { database, table } }
}

/** An S3 Tables table's page. */
export function s3TablesTableRoute(bucket: string, tableId: string): NodeRoute {
  return { to: "/s3tables/$bucket/$tableId", params: { bucket, tableId } }
}

/** One query execution, expanded in Athena's History tab. */
export function athenaExecutionRoute(executionId: string): NodeRoute {
  return { to: "/athena", search: { tab: "history", execution: executionId } }
}

/**
 * The click and middle-click handlers for a node or row that opens `route`.
 * A resource in another region switches the console to that region first, so
 * the page lands on it; a new tab carries the region in its URL instead.
 */
export function useNodeNavigation(route: NodeRoute | null, region?: string) {
  const navigate = useNavigate()
  const endpoint = useEndpoint()
  const open = useCallback(() => {
    if (!route) return
    if (region && region !== endpoint.region) {
      endpointStore.set({ ...endpoint, region })
    }
    void navigate({ to: route.to, params: route.params, search: route.search })
  }, [navigate, route, region, endpoint])
  const openInNewTab = useCallback(
    (e: React.MouseEvent) => {
      if (!route || e.button !== 1) return
      e.preventDefault()
      e.stopPropagation()
      openRouteInNewTab(route, { region: region ?? endpoint.region })
    },
    [route, region, endpoint.region],
  )
  return { open, openInNewTab }
}
