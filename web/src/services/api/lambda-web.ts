import {
  CreateWebFunctionCommand,
  CreateWebFunctionEndpointCommand,
  CreateWebFunctionRevisionCommand,
  DeleteResourcePolicyCommand,
  DeleteWebFunctionCommand,
  DeleteWebFunctionEndpointCommand,
  DeleteWebFunctionRevisionCommand,
  GetResourcePolicyCommand,
  GetWebAccountSettingsCommand,
  GetWebFunctionCommand,
  GetWebFunctionRevisionCommand,
  ListTagsCommand,
  PutResourcePolicyCommand,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateWebFunctionEndpointCommand,
  paginateListWebFunctionEndpoints,
  paginateListWebFunctionRevisions,
  paginateListWebFunctions,
  type CreateWebFunctionEndpointRequest,
  type CreateWebFunctionRequest,
  type CreateWebFunctionRevisionRequest,
  type FunctionEndpointSummary,
  type FunctionRevisionSummary,
  type FunctionSummary,
  type GetWebAccountSettingsResponse,
  type GetWebFunctionRevisionResponse,
  type UpdateWebFunctionEndpointRequest,
} from "@aws-sdk/client-lambda-web"
import { nullWhen } from "@/lib/aws-error"
import { awsClients } from "../aws-clients"
import { endpointResolver } from "./base"
import { collectPages } from "./paginate"

export type WebFunction = FunctionSummary
export type WebRevision = FunctionRevisionSummary
export type WebEndpoint = FunctionEndpointSummary
export type WebRevisionDetail = Omit<GetWebFunctionRevisionResponse, "$metadata">
export type WebAccountSettings = Omit<GetWebAccountSettingsResponse, "$metadata">
export type CreateWebFunctionInput = CreateWebFunctionRequest
export type CreateRevisionInput = CreateWebFunctionRevisionRequest
export type CreateEndpointInput = CreateWebFunctionEndpointRequest
export type UpdateEndpointInput = UpdateWebFunctionEndpointRequest

/** A function's resource policy and the revision id that guards changes to it. */
export interface WebPolicy {
  policy: string
  revisionId: string
}

/** One request to an endpoint, through the invoke path (`/_overcast/lambdaweb/invoke/…`). */
export interface EndpointRequest {
  /** The invoke path of the endpoint, without a trailing slash (see `invokePath`). */
  invokePath: string
  method: string
  /** The path and query to request on the endpoint, starting with `/`. */
  target: string
  headers: Record<string, string>
  body?: string
}

/** What an endpoint answered. Header names are lower case. */
export interface EndpointResponse {
  status: number
  statusText: string
  headers: [name: string, value: string][]
  body: Uint8Array
  /** Milliseconds from sending the request to reading the whole body. */
  durationMs: number
}

/** An optional configuration's absence is `ResourceNotFoundException`, not an empty answer. */
const orNone = <T>(call: Promise<T>) => nullWhen("ResourceNotFoundException", call)

const client = () => awsClients.lambdaWeb()

export const lambdaWeb = {
  /**
   * Sends a request to an endpoint through the invoke path, which works with
   * any Host (a browser cannot set one): the server forwards the endpoint's
   * own domain to the application. Any status is an answer, not an error — a
   * 502 is what a broken revision is expected to give — so only a network
   * failure throws. This is the emulator's own `/_overcast/` surface, which
   * has no SDK client, so it is `fetch`.
   */
  invokeEndpoint: async (request: EndpointRequest): Promise<EndpointResponse> => {
    const { baseUrl } = endpointResolver.get()
    const started = performance.now()
    const res = await fetch(
      `${baseUrl.replace(/\/+$/, "")}${request.invokePath}${request.target}`,
      {
        method: request.method,
        headers: request.headers,
        body: request.body === undefined || request.body === "" ? undefined : request.body,
      },
    )
    const body = new Uint8Array(await res.arrayBuffer())
    return {
      status: res.status,
      statusText: res.statusText,
      headers: [...res.headers.entries()],
      body,
      durationMs: performance.now() - started,
    }
  },

  listFunctions: (): Promise<WebFunction[]> =>
    collectPages(paginateListWebFunctions({ client: client() }, {}), (page) => page.functions),

  getFunction: async (functionName: string): Promise<WebFunction> => {
    const { $metadata: _, ...fn } = await client().send(new GetWebFunctionCommand({ functionName }))
    return fn
  },

  /** Creates the function, and the first revision and endpoint when the input carries them. */
  createFunction: async (input: CreateWebFunctionInput) => {
    const { $metadata: _, ...created } = await client().send(new CreateWebFunctionCommand(input))
    return created
  },

  deleteFunction: async (functionName: string): Promise<void> => {
    await client().send(new DeleteWebFunctionCommand({ functionName }))
  },

  listRevisions: (functionName: string): Promise<WebRevision[]> =>
    collectPages(
      paginateListWebFunctionRevisions({ client: client() }, { functionName }),
      (page) => page.revisions,
    ),

  getRevision: async (functionName: string, revisionId: string): Promise<WebRevisionDetail> => {
    const { $metadata: _, ...revision } = await client().send(
      new GetWebFunctionRevisionCommand({ functionName, revisionId }),
    )
    return revision
  },

  createRevision: async (input: CreateRevisionInput): Promise<string> =>
    (await client().send(new CreateWebFunctionRevisionCommand(input))).revisionId ?? "",

  deleteRevision: async (functionName: string, revisionId: string): Promise<void> => {
    await client().send(new DeleteWebFunctionRevisionCommand({ functionName, revisionId }))
  },

  listEndpoints: (functionName: string): Promise<WebEndpoint[]> =>
    collectPages(
      paginateListWebFunctionEndpoints({ client: client() }, { functionName }),
      (page) => page.endpoints,
    ),

  createEndpoint: async (input: CreateEndpointInput): Promise<string> =>
    (await client().send(new CreateWebFunctionEndpointCommand(input))).endpointName ?? "",

  updateEndpoint: async (input: UpdateEndpointInput): Promise<void> => {
    await client().send(new UpdateWebFunctionEndpointCommand(input))
  },

  deleteEndpoint: async (functionName: string, endpointName: string): Promise<void> => {
    await client().send(new DeleteWebFunctionEndpointCommand({ functionName, endpointName }))
  },

  /** The resource policy, or null when none is set. */
  getPolicy: async (resourceArn: string): Promise<WebPolicy | null> => {
    const answer = await orNone(client().send(new GetResourcePolicyCommand({ resourceArn })))
    return answer ? { policy: answer.policy ?? "", revisionId: answer.revisionId ?? "" } : null
  },

  putPolicy: async (
    resourceArn: string,
    policy: string,
    revisionId?: string,
  ): Promise<WebPolicy> => {
    const answer = await client().send(
      new PutResourcePolicyCommand({ resourceArn, policy, revisionId }),
    )
    return { policy: answer.policy ?? "", revisionId: answer.revisionId ?? "" }
  },

  deletePolicy: async (resourceArn: string): Promise<void> => {
    await client().send(new DeleteResourcePolicyCommand({ resourceArn }))
  },

  listTags: async (resource: string): Promise<Record<string, string>> =>
    (await client().send(new ListTagsCommand({ resource }))).tags ?? {},

  tagResource: async (resource: string, tags: Record<string, string>): Promise<void> => {
    await client().send(new TagResourceCommand({ resource, tags }))
  },

  untagResource: async (resource: string, tagKeys: string[]): Promise<void> => {
    await client().send(new UntagResourceCommand({ resource, tagKeys }))
  },

  getAccountSettings: async (): Promise<WebAccountSettings> => {
    const { $metadata: _, ...settings } = await client().send(new GetWebAccountSettingsCommand({}))
    return settings
  },
}
