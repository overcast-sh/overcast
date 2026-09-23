import { awsClients } from "../aws-clients"
import { apiFetch, endpointHeaders, API_BASE, endpointResolver } from "./base"
import {
  ListFunctionsCommand,
  GetFunctionCommand,
  CreateFunctionCommand,
  DeleteFunctionCommand,
  InvokeCommand,
  UpdateFunctionCodeCommand,
  UpdateFunctionConfigurationCommand,
  PublishVersionCommand,
  ListVersionsByFunctionCommand,
  CreateAliasCommand,
  ListAliasesCommand,
  UpdateAliasCommand,
  DeleteAliasCommand,
  ListLayersCommand,
  ListLayerVersionsCommand,
  GetLayerVersionCommand,
  PublishLayerVersionCommand,
  DeleteLayerVersionCommand,
  CreateEventSourceMappingCommand,
  ListEventSourceMappingsCommand,
  UpdateEventSourceMappingCommand,
  DeleteEventSourceMappingCommand,
  ListTagsCommand,
  TagResourceCommand,
  UntagResourceCommand,
  type CreateFunctionCommandInput,
  type UpdateFunctionConfigurationCommandInput,
  type CreateAliasCommandInput,
  type UpdateAliasCommandInput,
  type CreateEventSourceMappingCommandInput,
  type UpdateEventSourceMappingCommandInput,
  type Runtime,
} from "@aws-sdk/client-lambda"
import type {
  PutCodePayload,
  LambdaRuntimeInfo,
  LambdaFunctionSource,
  InvokeResult,
  SavedTestEvent,
  LambdaInstance,
  LambdaLayerVersionMetadata,
  MonitorResponse,
} from "@/types"
import type { ChartRangeToken } from "@/features/monitoring/types"

export type InvokeEvent =
  | { type: "progress"; step: string }
  | { type: "result"; data: InvokeResult }

export const lambdaInstances = {
  list: () =>
    apiFetch<{ instances: LambdaInstance[] }>("/lambda/instances").then((r) => r.instances),
}

export const lambda = {
  // ── Emulator-only (BFF) ──────────────────────────────────────────────────
  listRuntimes: () =>
    apiFetch<{ runtimes: LambdaRuntimeInfo[] }>("/lambda/runtimes").then((r) => r.runtimes),

  getLayerVersionMetadata: (layerName: string, version: number) =>
    apiFetch<LambdaLayerVersionMetadata>(
      `/lambda/layers/${encodeURIComponent(layerName)}/versions/${version}/metadata`,
    ),

  /** Monitor tab read-through — docs/plans/service-metrics-platform.md phase 3. */
  getMetrics: (name: string, range: ChartRangeToken) =>
    apiFetch<MonitorResponse>(
      `/lambda/functions/${encodeURIComponent(name)}/metrics?range=${range}`,
    ),

  getSource: (name: string, file?: string) => {
    const params = file ? `?file=${encodeURIComponent(file)}` : ""
    return apiFetch<LambdaFunctionSource>(
      `/lambda/functions/${encodeURIComponent(name)}/source${params}`,
    )
  },

  putSource: (name: string, source: string, filename: string) =>
    apiFetch<LambdaFunctionSource>(`/lambda/functions/${encodeURIComponent(name)}/source`, {
      method: "PUT",
      body: JSON.stringify({ source, filename }),
    }),

  invokeStream: async function* (
    name: string,
    payload: string,
    signal?: AbortSignal,
  ): AsyncGenerator<InvokeEvent> {
    const endpoint = endpointResolver.get()
    const res = await fetch(
      `${API_BASE}/lambda/functions/${encodeURIComponent(name)}/invoke-with-progress`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json", ...endpointHeaders(endpoint) },
        body: JSON.stringify({ payload }),
        signal,
      },
    )

    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => "Unknown error")
      throw new Error(text || `HTTP ${res.status}`)
    }

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ""
    let currentEvent = ""

    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) {
          // The stream closed before the result event. The invocation itself is
          // not necessarily lost — the connection is what went away — so point
          // at the logs rather than reporting a function failure.
          throw new Error(
            "Connection to the emulator closed before the invocation returned. " +
              "The function may still be running — check its CloudWatch logs.",
          )
        }

        buffer += decoder.decode(value, { stream: true })
        const parts = buffer.split("\n")
        buffer = parts.pop() ?? ""

        for (const line of parts) {
          if (line.startsWith("event: ")) {
            currentEvent = line.slice(7).trim()
          } else if (line.startsWith("data: ")) {
            const data = line.slice(6)
            if (currentEvent === "progress") {
              yield { type: "progress", step: data }
            } else if (currentEvent === "result") {
              yield { type: "result", data: JSON.parse(data) as InvokeResult }
              return
            } else if (currentEvent === "error") {
              throw new Error(data)
            }
            currentEvent = ""
          }
        }
      }
    } finally {
      reader.releaseLock()
    }
  },

  listTestEvents: (name: string) =>
    apiFetch<{ events: SavedTestEvent[] }>(
      `/lambda/functions/${encodeURIComponent(name)}/test-events`,
    ).then((r) => r.events),

  /** `region` saves it against another region's function than the console's. */
  putTestEvent: (functionName: string, eventName: string, body: string, region?: string) =>
    apiFetch<SavedTestEvent>(
      `/lambda/functions/${encodeURIComponent(functionName)}/test-events/${encodeURIComponent(eventName)}`,
      {
        method: "PUT",
        body: JSON.stringify({ body }),
        ...(region ? { headers: { "x-overcast-region": region } } : {}),
      },
    ),

  deleteTestEvent: (functionName: string, eventName: string) =>
    apiFetch<Record<string, never>>(
      `/lambda/functions/${encodeURIComponent(functionName)}/test-events/${encodeURIComponent(eventName)}`,
      { method: "DELETE" },
    ),

  // ── Standard AWS SDK ops ─────────────────────────────────────────────────
  listFunctions: async () => {
    const res = await awsClients.lambda().send(new ListFunctionsCommand({}))
    return res.Functions ?? []
  },

  createFunction: async (params: CreateFunctionCommandInput) =>
    await awsClients.lambda().send(new CreateFunctionCommand(params)),

  getFunction: async (name: string, region?: string) => {
    const res = await awsClients.lambda(region).send(new GetFunctionCommand({ FunctionName: name }))
    return res.Configuration ?? {}
  },

  // Qualifier deletes only that published version; without one the whole
  // function goes, versions and aliases included.
  deleteFunction: async (name: string, qualifier?: string) => {
    await awsClients
      .lambda()
      .send(new DeleteFunctionCommand({ FunctionName: name, Qualifier: qualifier }))
  },

  // Tags hang off the unqualified function ARN — never a version or an alias.
  listTags: async (resourceArn: string) => {
    const res = await awsClients.lambda().send(new ListTagsCommand({ Resource: resourceArn }))
    return res.Tags ?? {}
  },

  tagResource: async (resourceArn: string, tags: Record<string, string>) => {
    await awsClients.lambda().send(new TagResourceCommand({ Resource: resourceArn, Tags: tags }))
  },

  untagResource: async (resourceArn: string, tagKeys: string[]) => {
    await awsClients
      .lambda()
      .send(new UntagResourceCommand({ Resource: resourceArn, TagKeys: tagKeys }))
  },

  invoke: async (name: string, payload: string) => {
    const res = await awsClients
      .lambda()
      .send(new InvokeCommand({ FunctionName: name, Payload: new TextEncoder().encode(payload) }))
    return {
      statusCode: res.StatusCode ?? 200,
      payload: res.Payload ? new TextDecoder().decode(res.Payload) : null,
      functionError: res.FunctionError ?? null,
      logResult: res.LogResult ?? null,
      executedVersion: res.ExecutedVersion ?? "$LATEST",
      logGroupName: null,
      logStreamName: null,
    } satisfies InvokeResult
  },

  updateFunctionConfiguration: async (params: UpdateFunctionConfigurationCommandInput) =>
    await awsClients.lambda().send(new UpdateFunctionConfigurationCommand(params)),

  updateFunctionLayers: async (functionName: string, layerArns: string[]) =>
    await awsClients
      .lambda()
      .send(
        new UpdateFunctionConfigurationCommand({ FunctionName: functionName, Layers: layerArns }),
      ),

  putCode: async (name: string, payload: PutCodePayload) => {
    const client = awsClients.lambda()
    if (payload.type === "zip") {
      const bytes = new Uint8Array(await payload.file.arrayBuffer())
      await client.send(
        new UpdateFunctionCodeCommand({
          FunctionName: name,
          ZipFile: bytes,
          SourceKMSKeyArn: payload.sourceKMSKeyArn,
        }),
      )
    } else if (payload.type === "s3") {
      await client.send(
        new UpdateFunctionCodeCommand({
          FunctionName: name,
          S3Bucket: payload.s3Bucket,
          S3Key: payload.s3Key,
          S3ObjectVersion: payload.s3ObjectVersion,
          SourceKMSKeyArn: payload.sourceKMSKeyArn,
        }),
      )
    } else {
      await client.send(
        new UpdateFunctionCodeCommand({
          FunctionName: name,
          ImageUri: payload.imageUri,
          SourceKMSKeyArn: payload.sourceKMSKeyArn,
        }),
      )
    }
  },

  listVersions: async (name: string) => {
    const res = await awsClients
      .lambda()
      .send(new ListVersionsByFunctionCommand({ FunctionName: name }))
    return res.Versions ?? []
  },

  publishVersion: async (name: string, Description?: string) =>
    await awsClients.lambda().send(new PublishVersionCommand({ FunctionName: name, Description })),

  listAliases: async (name: string) => {
    const res = await awsClients.lambda().send(new ListAliasesCommand({ FunctionName: name }))
    return res.Aliases ?? []
  },

  createAlias: async (params: CreateAliasCommandInput) =>
    await awsClients.lambda().send(new CreateAliasCommand(params)),

  updateAlias: async (params: UpdateAliasCommandInput) =>
    await awsClients.lambda().send(new UpdateAliasCommand(params)),

  deleteAlias: async (functionName: string, aliasName: string) => {
    await awsClients
      .lambda()
      .send(new DeleteAliasCommand({ FunctionName: functionName, Name: aliasName }))
  },

  listLayers: async () => {
    const res = await awsClients.lambda().send(new ListLayersCommand({}))
    return res.Layers ?? []
  },

  listLayerVersions: async (layerName: string) => {
    const res = await awsClients
      .lambda()
      .send(new ListLayerVersionsCommand({ LayerName: layerName }))
    return res.LayerVersions ?? []
  },

  getLayerVersion: async (layerName: string, version: number) =>
    await awsClients
      .lambda()
      .send(new GetLayerVersionCommand({ LayerName: layerName, VersionNumber: version })),

  publishLayerVersion: async (params: {
    layerName: string
    description?: string
    zipFile?: string
    compatibleRuntimes?: string[]
    compatibleArchitectures?: string[]
  }) => {
    const zipBytes = params.zipFile
      ? Uint8Array.from(atob(params.zipFile), (ch) => ch.charCodeAt(0))
      : new Uint8Array(0)
    return await awsClients.lambda().send(
      new PublishLayerVersionCommand({
        LayerName: params.layerName,
        Description: params.description,
        Content: { ZipFile: zipBytes },
        CompatibleRuntimes: params.compatibleRuntimes as Runtime[],
        CompatibleArchitectures: params.compatibleArchitectures as ("x86_64" | "arm64")[],
      }),
    )
  },

  deleteLayerVersion: async (layerName: string, version: number) => {
    await awsClients
      .lambda()
      .send(new DeleteLayerVersionCommand({ LayerName: layerName, VersionNumber: version }))
  },

  listEventSourceMappings: async (functionName?: string) => {
    const res = await awsClients
      .lambda()
      .send(new ListEventSourceMappingsCommand({ FunctionName: functionName || undefined }))
    return res.EventSourceMappings ?? []
  },

  createEventSourceMapping: async (params: CreateEventSourceMappingCommandInput) =>
    await awsClients.lambda().send(new CreateEventSourceMappingCommand(params)),

  updateEventSourceMapping: async (
    uuid: string,
    params: Omit<UpdateEventSourceMappingCommandInput, "UUID">,
  ) =>
    await awsClients.lambda().send(new UpdateEventSourceMappingCommand({ UUID: uuid, ...params })),

  deleteEventSourceMapping: async (uuid: string) =>
    await awsClients.lambda().send(new DeleteEventSourceMappingCommand({ UUID: uuid })),
}
