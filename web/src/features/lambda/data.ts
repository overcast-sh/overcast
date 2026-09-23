/**
 * Lambda query/mutation definitions.
 *
 * Key factory:
 *   lambdaKeys.all()               -> ["lambda"]
 *   lambdaKeys.functions()       -> ["lambda", "functions"]
 *   lambdaKeys.functionList(url) -> ["lambda", "functions", url]
 *   lambdaKeys.source()          -> ["lambda", "source"]
 *   lambdaKeys.sourceFiles(name) -> ["lambda", "source", name]
 */

import { queryOptions, mutationOptions } from "@tanstack/react-query"
import { lambda } from "@/services/api"
import type {
  CreateFunctionCommandInput,
  UpdateFunctionConfigurationCommandInput,
  CreateAliasCommandInput,
  UpdateAliasCommandInput,
  CreateEventSourceMappingCommandInput,
  UpdateEventSourceMappingCommandInput,
} from "@aws-sdk/client-lambda"
import type { InvokeResult, SavedTestEvent } from "@/types"
import { endpointStore } from "@/services/endpoint-store"
import type { ChartRangeToken } from "@/features/monitoring/types"

// ─── Key factory ───────────────────────────────────────────────────────────

export const lambdaKeys = {
  all: () => [...endpointStore.getKeys(), "lambda"] as const,
  runtimes: () => [...lambdaKeys.all(), "runtimes"] as const,
  functions: () => [...lambdaKeys.all(), "functions"] as const,
  function: (name: string, region = "") =>
    [...lambdaKeys.functions(), "detail", name, region] as const,
  source: () => [...lambdaKeys.all(), "source"] as const,
  sourceFiles: (name: string) => [...lambdaKeys.source(), name] as const,
  sourceFile: (name: string, file: string) => [...lambdaKeys.all(), "source", name, file] as const,
  testEvents: (name: string) => [...lambdaKeys.all(), "test-events", name] as const,
  versions: (name: string) => [...lambdaKeys.all(), "versions", name] as const,
  aliases: (name: string) => [...lambdaKeys.all(), "aliases", name] as const,
  layers: () => [...lambdaKeys.all(), "layers"] as const,
  layerVersions: (layerName: string) => [...lambdaKeys.layers(), layerName, "versions"] as const,
  layerVersionMetadata: (layerName: string, version: number) =>
    [...lambdaKeys.layerVersions(layerName), version, "metadata"] as const,
  esms: (functionName: string) => [...lambdaKeys.all(), "esms", functionName] as const,
  tags: (resourceArn: string) => [...lambdaKeys.all(), "tags", resourceArn] as const,
  metrics: (name: string, range: ChartRangeToken) =>
    [...lambdaKeys.all(), "metrics", name, range] as const,
}

// ─── Query definitions ─────────────────────────────────────────────────────

export function lambdaRuntimesQueryOptions() {
  return queryOptions({
    queryKey: lambdaKeys.runtimes(),
    queryFn: () => lambda.listRuntimes(),
    staleTime: Infinity,
  })
}

export function lambdaFunctionsQueryOptions() {
  return queryOptions({
    queryKey: lambdaKeys.functions(),
    queryFn: () => lambda.listFunctions(),
  })
}

/**
 * One function's configuration — for a page that only knows its name, such as
 * a Step Functions Task that called it. Not retried: a function that does not
 * exist is an answer, not a transient failure.
 */
export function lambdaFunctionQueryOptions(name: string, region?: string) {
  return queryOptions({
    queryKey: lambdaKeys.function(name, region),
    queryFn: () => lambda.getFunction(name, region),
    enabled: name !== "",
    retry: false,
    staleTime: 30_000,
  })
}

/**
 * Monitor tab read-through (docs/plans/service-metrics-platform.md phase 3).
 * Polls while the tab is visible — the plan's "Use polling initially" —
 * rather than a manual-refresh-only chart.
 */
export function lambdaMetricsQueryOptions(
  name: string,
  range: ChartRangeToken,
  refetchIntervalMs: number | false = 30_000,
) {
  return queryOptions({
    queryKey: lambdaKeys.metrics(name, range),
    queryFn: () => lambda.getMetrics(name, range),
    refetchInterval: refetchIntervalMs,
    enabled: Boolean(name),
  })
}

// ─── Mutation definitions ──────────────────────────────────────────────────

export function createFunctionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.functions(), "create"] as const,
    mutationFn: (params: CreateFunctionCommandInput) => lambda.createFunction(params),
  })
}

/**
 * Deletes a whole function, or — with a qualifier — only that published
 * version. AWS refuses a qualifier that names $LATEST or a version an alias
 * references, so the error surfaces to the caller rather than being swallowed.
 */
export function deleteFunctionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.functions(), "delete"] as const,
    mutationFn: ({ name, qualifier }: { name: string; qualifier?: string }) =>
      lambda.deleteFunction(name, qualifier),
  })
}

// ─── Tags ──────────────────────────────────────────────────────────────────

export function functionTagsQueryOptions(resourceArn: string) {
  return queryOptions({
    queryKey: lambdaKeys.tags(resourceArn),
    queryFn: () => lambda.listTags(resourceArn),
    enabled: !!resourceArn,
  })
}

/**
 * Saves an edited tag set as the two AWS calls it really is: TagResource for
 * added or changed keys, UntagResource for removed ones. TagResource merges,
 * so a key dropped in the editor has to be untagged explicitly.
 */
export function updateFunctionTagsMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "tags", "update"] as const,
    mutationFn: async ({
      resourceArn,
      tags,
      previous,
    }: {
      resourceArn: string
      tags: Record<string, string>
      previous: Record<string, string>
    }) => {
      const removed = Object.keys(previous).filter((key) => !(key in tags))
      if (removed.length > 0) await lambda.untagResource(resourceArn, removed)
      const changed = Object.fromEntries(
        Object.entries(tags).filter(([key, value]) => previous[key] !== value),
      )
      if (Object.keys(changed).length > 0) await lambda.tagResource(resourceArn, changed)
    },
  })
}

// ─── Source ────────────────────────────────────────────────────────────────

export function lambdaSourceQueryOptions(name: string) {
  return queryOptions({
    queryKey: lambdaKeys.sourceFiles(name),
    queryFn: () => lambda.getSource(name),
    enabled: !!name,
  })
}

export function lambdaSourceFileQueryOptions(name: string, file: string) {
  return queryOptions({
    queryKey: lambdaKeys.sourceFile(name, file),
    queryFn: () => lambda.getSource(name, file),
    enabled: !!name && !!file,
  })
}

export function putSourceMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "source", "put"] as const,
    mutationFn: ({ name, source, filename }: { name: string; source: string; filename: string }) =>
      lambda.putSource(name, source, filename),
  })
}

export function invokeFunctionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "invoke"] as const,
    mutationFn: ({ name, payload }: { name: string; payload: string }): Promise<InvokeResult> =>
      lambda.invoke(name, payload),
  })
}

// ─── Test events ──────────────────────────────────────────────────────────

export function testEventsQueryOptions(functionName: string) {
  return queryOptions({
    queryKey: lambdaKeys.testEvents(functionName),
    queryFn: () => lambda.listTestEvents(functionName),
    enabled: !!functionName,
  })
}

export function putTestEventMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "test-events", "put"] as const,
    mutationFn: ({
      functionName,
      eventName,
      body,
      region,
    }: {
      functionName: string
      eventName: string
      body: string
      region?: string
    }): Promise<SavedTestEvent> => lambda.putTestEvent(functionName, eventName, body, region),
  })
}

export function deleteTestEventMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "test-events", "delete"] as const,
    mutationFn: ({ functionName, eventName }: { functionName: string; eventName: string }) =>
      lambda.deleteTestEvent(functionName, eventName),
  })
}

// ─── Versions ─────────────────────────────────────────────────────────────

export function versionsQueryOptions(name: string) {
  return queryOptions({
    queryKey: lambdaKeys.versions(name),
    queryFn: () => lambda.listVersions(name),
    enabled: !!name,
  })
}

export function publishVersionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "versions", "publish"] as const,
    mutationFn: ({ name, description }: { name: string; description?: string }) =>
      lambda.publishVersion(name, description),
  })
}

// ─── Aliases ──────────────────────────────────────────────────────────────

export function aliasesQueryOptions(name: string) {
  return queryOptions({
    queryKey: lambdaKeys.aliases(name),
    queryFn: () => lambda.listAliases(name),
    enabled: !!name,
  })
}

export function createAliasMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "aliases", "create"] as const,
    mutationFn: (params: CreateAliasCommandInput) => lambda.createAlias(params),
  })
}

export function updateAliasMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "aliases", "update"] as const,
    mutationFn: (params: UpdateAliasCommandInput) => lambda.updateAlias(params),
  })
}

export function deleteAliasMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "aliases", "delete"] as const,
    mutationFn: ({ functionName, aliasName }: { functionName: string; aliasName: string }) =>
      lambda.deleteAlias(functionName, aliasName),
  })
}

// ─── Layers ────────────────────────────────────────────────────────────────

export function layersQueryOptions() {
  return queryOptions({
    queryKey: lambdaKeys.layers(),
    queryFn: () => lambda.listLayers(),
  })
}

export function layerVersionsQueryOptions(layerName: string) {
  return queryOptions({
    queryKey: lambdaKeys.layerVersions(layerName),
    queryFn: () => lambda.listLayerVersions(layerName),
    enabled: !!layerName,
  })
}

export function layerVersionMetadataQueryOptions(layerName: string, version: number) {
  return queryOptions({
    queryKey: lambdaKeys.layerVersionMetadata(layerName, version),
    queryFn: () => lambda.getLayerVersionMetadata(layerName, version),
    enabled: !!layerName && version > 0,
    staleTime: Infinity,
  })
}

export function publishLayerVersionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.layers(), "publish"] as const,
    mutationFn: (params: {
      layerName: string
      description?: string
      zipFile?: string
      compatibleRuntimes?: string[]
      compatibleArchitectures?: string[]
    }) => lambda.publishLayerVersion(params),
  })
}

export function deleteLayerVersionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.layers(), "delete"] as const,
    mutationFn: ({ layerName, version }: { layerName: string; version: number }) =>
      lambda.deleteLayerVersion(layerName, version),
  })
}

export function updateFunctionLayersMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.functions(), "update-layers"] as const,
    mutationFn: ({ functionName, layerArns }: { functionName: string; layerArns: string[] }) =>
      lambda.updateFunctionLayers(functionName, layerArns),
  })
}

export function updateFunctionConfigurationMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.functions(), "update-config"] as const,
    mutationFn: (params: UpdateFunctionConfigurationCommandInput) =>
      lambda.updateFunctionConfiguration(params),
  })
}

// ─── Event Source Mappings ─────────────────────────────────────────────────

export function esmsQueryOptions(functionName: string) {
  return queryOptions({
    queryKey: lambdaKeys.esms(functionName),
    queryFn: () => lambda.listEventSourceMappings(functionName),
    enabled: !!functionName,
  })
}

export function createEsmMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "esms", "create"] as const,
    mutationFn: (params: CreateEventSourceMappingCommandInput) =>
      lambda.createEventSourceMapping(params),
  })
}

export function updateEsmMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "esms", "update"] as const,
    mutationFn: ({
      uuid,
      ...params
    }: { uuid: string } & Omit<UpdateEventSourceMappingCommandInput, "UUID">) =>
      lambda.updateEventSourceMapping(uuid, params),
  })
}

export function deleteEsmMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaKeys.all(), "esms", "delete"] as const,
    mutationFn: (uuid: string) => lambda.deleteEventSourceMapping(uuid),
  })
}
