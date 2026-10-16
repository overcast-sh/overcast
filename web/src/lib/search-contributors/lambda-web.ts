import { lambdaWeb } from "@/services/api"
import type { WebEndpoint, WebFunction } from "@/services/api/lambda-web"
import { lambdaWebKeys } from "@/features/lambda-web/data"
import { createSearchContributor } from "./create-contributor"

const functionHref = (name: string) => `/lambda-web/${encodeURIComponent(name)}`

/** An endpoint with the name of the function it belongs to: the listing carries only its ARN. */
type InFunction<T> = T & { functionName: string }

createSearchContributor<WebFunction>({
  id: "lambda-web:functions",
  cacheKey: () => lambdaWebKeys.functions(),
  fetchAll: () => lambdaWeb.listFunctions(),
  matchFields: (f) => [f.functionName, f.functionArn],
  toResult: (f) => ({
    id: `lambda-web:function:${f.functionName}`,
    label: f.functionName ?? "",
    sublabel: f.functionArn,
    service: "Lambda Web Functions",
    serviceKey: "/lambda-web",
    type: "Web function",
    href: functionHref(f.functionName ?? ""),
  }),
})

createSearchContributor<InFunction<WebEndpoint>>({
  id: "lambda-web:endpoints",
  cacheKey: () => lambdaWebKeys.allEndpoints(),
  fetchAll: async () => {
    const functions = await lambdaWeb.listFunctions()
    const perFunction = await Promise.all(
      functions.map(async (f) =>
        (await lambdaWeb.listEndpoints(f.functionName ?? "")).map((e) => ({
          ...e,
          functionName: f.functionName ?? "",
        })),
      ),
    )
    return perFunction.flat()
  },
  matchFields: (e) => [e.endpointName, e.endpointArn, e.domainName],
  toResult: (e) => ({
    id: `lambda-web:endpoint:${e.endpointArn}`,
    label: e.endpointName ?? "",
    sublabel: e.functionName,
    service: "Lambda Web Functions",
    serviceKey: "/lambda-web",
    type: "Endpoint",
    href: `${functionHref(e.functionName)}?${new URLSearchParams({ tab: "endpoints" }).toString()}`,
  }),
})
