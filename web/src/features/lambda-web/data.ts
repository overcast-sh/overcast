/**
 * Lambda Web Functions query keys, query options and mutations.
 *
 * Key factory:
 *   lambdaWebKeys.all()                  -> [...endpoint, "lambda-web"]
 *   lambdaWebKeys.functions()            -> [...all(), "functions"]
 *   lambdaWebKeys.function(name)         -> [...functions(), name]
 *   lambdaWebKeys.revisions()            -> [...all(), "revisions"]
 *   lambdaWebKeys.functionRevisions(n)   -> [...revisions(), n]
 *   lambdaWebKeys.revision(n, id)        -> [...revisions(), n, id]
 *   lambdaWebKeys.endpoints()            -> [...all(), "endpoints"]
 *   lambdaWebKeys.functionEndpoints(n)   -> [...endpoints(), n]
 *   lambdaWebKeys.allEndpoints()         -> [...endpoints(), "all"]
 *   lambdaWebKeys.policy(arn)            -> [...all(), "policy", arn]
 *   lambdaWebKeys.tags(arn)              -> [...all(), "tags", arn]
 *   lambdaWebKeys.settings()             -> [...all(), "settings"]
 *
 * Revisions and endpoints each hang under one prefix, the one the
 * `lambda-web:Revision*` and `lambda-web:Endpoint*` events invalidate: a new
 * revision can move an endpoint's weights (`LatestRevision`), so a revision
 * event also re-reads the endpoints.
 */

import { mutationOptions, queryOptions } from "@tanstack/react-query"
import { lambdaWeb } from "@/services/api"
import type {
  CreateEndpointInput,
  CreateRevisionInput,
  CreateWebFunctionInput,
  UpdateEndpointInput,
} from "@/services/api/lambda-web"
import { endpointStore } from "@/services/endpoint-store"

/** The detail page's tabs. */
export const WEB_FUNCTION_TABS = ["overview", "revisions", "endpoints", "policy"] as const
export type WebFunctionTab = (typeof WEB_FUNCTION_TABS)[number]

export const lambdaWebKeys = {
  all: () => [...endpointStore.getKeys(), "lambda-web"] as const,
  functions: () => [...lambdaWebKeys.all(), "functions"] as const,
  function: (name: string) => [...lambdaWebKeys.functions(), name] as const,
  revisions: () => [...lambdaWebKeys.all(), "revisions"] as const,
  functionRevisions: (name: string) => [...lambdaWebKeys.revisions(), name] as const,
  revision: (name: string, id: string) => [...lambdaWebKeys.revisions(), name, id] as const,
  endpoints: () => [...lambdaWebKeys.all(), "endpoints"] as const,
  functionEndpoints: (name: string) => [...lambdaWebKeys.endpoints(), name] as const,
  allEndpoints: () => [...lambdaWebKeys.endpoints(), "all"] as const,
  policy: (arn: string) => [...lambdaWebKeys.all(), "policy", arn] as const,
  tags: (arn: string) => [...lambdaWebKeys.all(), "tags", arn] as const,
  settings: () => [...lambdaWebKeys.all(), "settings"] as const,
}

// ─── Queries ──────────────────────────────────────────────────────────────

export function webFunctionsQueryOptions() {
  return queryOptions({
    queryKey: lambdaWebKeys.functions(),
    queryFn: () => lambdaWeb.listFunctions(),
  })
}

export function webFunctionQueryOptions(name: string) {
  return queryOptions({
    queryKey: lambdaWebKeys.function(name),
    queryFn: () => lambdaWeb.getFunction(name),
    enabled: name !== "",
  })
}

export function webRevisionsQueryOptions(name: string) {
  return queryOptions({
    queryKey: lambdaWebKeys.functionRevisions(name),
    queryFn: () => lambdaWeb.listRevisions(name),
    enabled: name !== "",
  })
}

export function webRevisionQueryOptions(name: string, id: string) {
  return queryOptions({
    queryKey: lambdaWebKeys.revision(name, id),
    queryFn: () => lambdaWeb.getRevision(name, id),
    enabled: name !== "" && id !== "",
  })
}

export function webEndpointsQueryOptions(name: string) {
  return queryOptions({
    queryKey: lambdaWebKeys.functionEndpoints(name),
    queryFn: () => lambdaWeb.listEndpoints(name),
    enabled: name !== "",
  })
}

export function webPolicyQueryOptions(arn: string) {
  return queryOptions({
    queryKey: lambdaWebKeys.policy(arn),
    queryFn: () => lambdaWeb.getPolicy(arn),
    enabled: arn !== "",
  })
}

export function webTagsQueryOptions(arn: string) {
  return queryOptions({
    queryKey: lambdaWebKeys.tags(arn),
    queryFn: () => lambdaWeb.listTags(arn),
    enabled: arn !== "",
  })
}

export function webAccountSettingsQueryOptions() {
  return queryOptions({
    queryKey: lambdaWebKeys.settings(),
    queryFn: () => lambdaWeb.getAccountSettings(),
  })
}

// ─── Mutations ────────────────────────────────────────────────────────────

export function createWebFunctionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.functions(), "create"] as const,
    mutationFn: (input: CreateWebFunctionInput) => lambdaWeb.createFunction(input),
  })
}

export function deleteWebFunctionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.functions(), "delete"] as const,
    mutationFn: (name: string) => lambdaWeb.deleteFunction(name),
  })
}

export function createRevisionMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.revisions(), "create"] as const,
    mutationFn: (input: CreateRevisionInput) => lambdaWeb.createRevision(input),
  })
}

export function deleteRevisionMutationOptions(functionName: string) {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.functionRevisions(functionName), "delete"] as const,
    mutationFn: (revisionId: string) => lambdaWeb.deleteRevision(functionName, revisionId),
  })
}

export function createEndpointMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.endpoints(), "create"] as const,
    mutationFn: (input: CreateEndpointInput) => lambdaWeb.createEndpoint(input),
  })
}

export function updateEndpointMutationOptions() {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.endpoints(), "update"] as const,
    mutationFn: (input: UpdateEndpointInput) => lambdaWeb.updateEndpoint(input),
  })
}

export function deleteEndpointMutationOptions(functionName: string) {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.functionEndpoints(functionName), "delete"] as const,
    mutationFn: (endpointName: string) => lambdaWeb.deleteEndpoint(functionName, endpointName),
  })
}

export function putPolicyMutationOptions(arn: string) {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.policy(arn), "put"] as const,
    mutationFn: (vars: { policy: string; revisionId?: string }) =>
      lambdaWeb.putPolicy(arn, vars.policy, vars.revisionId),
  })
}

export function deletePolicyMutationOptions(arn: string) {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.policy(arn), "delete"] as const,
    mutationFn: () => lambdaWeb.deletePolicy(arn),
  })
}

export function tagResourceMutationOptions(arn: string) {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.tags(arn), "tag"] as const,
    mutationFn: (tags: Record<string, string>) => lambdaWeb.tagResource(arn, tags),
  })
}

export function untagResourceMutationOptions(arn: string) {
  return mutationOptions({
    mutationKey: [...lambdaWebKeys.tags(arn), "untag"] as const,
    mutationFn: (keys: string[]) => lambdaWeb.untagResource(arn, keys),
  })
}
