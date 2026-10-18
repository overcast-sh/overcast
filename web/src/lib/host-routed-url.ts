/**
 * Host-routed AWS URLs — the console side of the grammar the emulator routes on.
 *
 *   {scheme}://{id}.{label}.{region}.{host}[:{port}]{path}
 *
 * This is the same shape `serviceutil.HostRoutedURLFromBase` mints server-side
 * and `middleware.ParseHostRoute` dispatches on. Fields the server owns
 * (Lambda `FunctionUrl`, API Gateway v2 `apiEndpoint`, AppSync `uris`) already
 * arrive in this shape; the console composes its own only where AWS has no
 * field to carry one — REST (v1) invoke URLs, which the real console also
 * composes client-side.
 *
 * See docs/plans/host-routing-precedence.md §8.
 */

/** Labels the emulator's host-route table dispatches on. */
export const HOST_ROUTE_LABELS = {
  executeApi: "execute-api",
  lambdaUrl: "lambda-url",
  appsyncApi: "appsync-api",
  cloudfront: "cloudfront",
  lambdaWeb: "lambda-web",
} as const

export type HostRouteLabel = (typeof HOST_ROUTE_LABELS)[keyof typeof HOST_ROUTE_LABELS]

/**
 * Whether a host-routed URL minted on this base would actually resolve for the
 * user holding it.
 *
 * Host routing needs the base to carry an arbitrary subdomain. An IP literal
 * cannot, and a bare single-label name (`localhost`, a Docker service alias)
 * only resolves under wildcard DNS that Windows and macOS do not provide for
 * `*.localhost`. In both cases the caller must fall back to path-style
 * addressing, which the emulator still serves.
 */
export function supportsHostRouting(baseUrl: string): boolean {
  let hostname: string
  try {
    hostname = new URL(baseUrl).hostname
  } catch {
    return false
  }
  // URL.hostname brackets IPv6 literals; IPv4 is four dotted numeric labels.
  if (hostname.startsWith("[")) return false
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(hostname)) return false
  return hostname.includes(".")
}

/**
 * Mints `{scheme}://{id}.{label}.{region}.{host}[:{port}]{path}` on `baseUrl`,
 * or `undefined` when {@link supportsHostRouting} rules the base out.
 *
 * `region` may be empty — the emulator accepts the region-less form — and the
 * segment is then omitted rather than left blank. `path` is appended verbatim
 * and may be empty.
 */
export function hostRoutedUrl(
  baseUrl: string,
  label: HostRouteLabel,
  id: string,
  region: string,
  path: string,
): string | undefined {
  if (!supportsHostRouting(baseUrl)) return undefined
  let url: URL
  try {
    url = new URL(baseUrl)
  } catch {
    return undefined
  }
  const segments = region ? [id, label, region, url.hostname] : [id, label, url.hostname]
  const port = url.port ? `:${url.port}` : ""
  return `${url.protocol}//${segments.join(".")}${port}${path}`
}
