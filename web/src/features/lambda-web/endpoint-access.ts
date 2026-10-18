/**
 * How a person reaches an endpoint: the URL on its domain, the invoke path
 * that works wherever the console can reach the emulator, and what a failed
 * request's error type means.
 *
 * An endpoint's `domainName` is minted on the host the caller reached the
 * emulator on — `{domain-id}.{shard}.lambda-web.{region}.{host}`, no port
 * (internal/services/lambdaweb/domain.go). A browser can open it where that
 * host takes subdomains (`supportsHostRouting`). The invoke path
 * `/_overcast/lambdaweb/invoke/{domain-id}/{region}/…` reaches the same
 * endpoint with any Host, and the server forwards the endpoint's own domain
 * to the application as `Host`.
 */

import { HOST_ROUTE_LABELS, supportsHostRouting } from "@/lib/host-routed-url"

const MARKER = `.${HOST_ROUTE_LABELS.lambdaWeb}.`

/** The part of `domainName` before `.lambda-web.`: the routing key, `{domain-id}.{shard}`. */
export function domainIdOf(domainName: string): string {
  const i = domainName.toLowerCase().indexOf(MARKER)
  return i > 0 ? domainName.slice(0, i) : domainName
}

/**
 * The Region segment the invoke path wants. A MultiRegion endpoint's single
 * domain carries `global` (or none) there, which the path spells `-`.
 */
export function invokeRegionOf(domainName: string, fallbackRegion: string): string {
  const i = domainName.toLowerCase().indexOf(MARKER)
  if (i < 0) return fallbackRegion || "-"
  const segment = domainName.slice(i + MARKER.length).split(".")[0]
  if (segment === "global" || segment === "") return "-"
  // `lambda-web.{host}` with no Region: the first label is the host's.
  return segment === fallbackRegion || /^[a-z]{2,}(-[a-z]+)+-\d+$/.test(segment)
    ? segment
    : fallbackRegion || "-"
}

/** `/_overcast/lambdaweb/invoke/{domain-id}/{region}` — no trailing slash; a request path is appended. */
export function invokePath(domainName: string, fallbackRegion: string): string {
  return `/_overcast/lambdaweb/invoke/${domainIdOf(domainName)}/${invokeRegionOf(domainName, fallbackRegion)}`
}

export interface EndpointAddress {
  /**
   * The endpoint on its own domain — what a browser or `curl` can open — when
   * the host the console reaches the emulator on takes subdomains and the
   * domain was minted on it. Undefined otherwise.
   */
  hostUrl?: string
  /** The invoke URL: works with any Host, so it always works. */
  invokeUrl: string
}

/**
 * The URLs of an endpoint, given the emulator's base URL as the console
 * reaches it. The port is the base URL's: a domain name never carries one.
 */
export function endpointAddress(
  domainName: string,
  baseUrl: string,
  fallbackRegion: string,
): EndpointAddress {
  const invokeUrl = `${baseUrl.replace(/\/+$/, "")}${invokePath(domainName, fallbackRegion)}/`
  let url: URL
  try {
    url = new URL(baseUrl)
  } catch {
    return { invokeUrl }
  }
  const onThisHost = domainName.toLowerCase().endsWith(`.${url.hostname.toLowerCase()}`)
  if (!supportsHostRouting(baseUrl) || !onThisHost) return { invokeUrl }
  return { hostUrl: `${url.protocol}//${domainName}${url.port ? `:${url.port}` : ""}/`, invokeUrl }
}

/**
 * A `curl` command that calls an `IamAuth` endpoint: the endpoint wants a
 * SigV4 signature (service `lambda`, the endpoint's Region) and the payload
 * hash header. Any credentials do unless the emulator enforces signatures.
 */
export function curlSigV4Example(url: string, region: string): string {
  return [
    `curl --aws-sigv4 "aws:amz:${region || "us-east-1"}:lambda" \\`,
    `  --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \\`,
    `  -H "x-amz-content-sha256: UNSIGNED-PAYLOAD" \\`,
    `  "${url}"`,
  ].join("\n")
}

/**
 * What a `lambda-web-error-type` means, in a sentence: the "Web Function error
 * types" table of the Lambda Developer Guide, with the cause in Overcast's
 * terms (internal/services/lambdaweb/errors.go).
 */
const ERROR_TYPES: Record<string, string> = {
  "Platform.MalformedRequest":
    "The request could not be understood: an unsupported method or malformed syntax.",
  "Platform.MissingHostHeader": "The request had no Host header.",
  "Platform.InvalidEndpoint": "No endpoint is served at this domain. It may have been deleted.",
  "Platform.HeaderTooLarge": "The request headers exceed 128 KiB.",
  "Platform.DisallowedHeader": "The request carried a header that endpoints do not accept.",
  "Platform.UnsupportedHttpVersion": "Endpoints speak HTTP/1.1 only.",
  "Platform.CannotAssumeRole":
    "Lambda could not assume the revision's execution role. Check its trust policy.",
  "Platform.KMSDecryptionError": "Lambda could not decrypt the revision with its KMS key.",
  "Platform.InvalidNetworkConfiguration": "The revision's network connector is missing or invalid.",
  "Platform.RevisionNotActive":
    "The revision this endpoint routes to is not Active: still Pending, Failed, or its package is gone.",
  "Platform.EndpointNotActive": "The endpoint is not Active yet, or is being deleted.",
  "Platform.EndpointDisabled": "The endpoint is disabled: its rate limit is 0 requests per second.",
  "Platform.AccountDisabled": "Web functions are disabled for this account.",
  "Platform.AccountRequestThrottle": "The account's request rate limit was exceeded.",
  "Platform.EndpointRequestThrottle":
    "The endpoint's rate limit (throttleConfig.rateLimit) was exceeded. Retry after a second.",
  "Platform.AccountActiveVCPUArm64Throttle":
    "The account (or this host) has no room for another execution environment.",
  "Platform.EndpointMaxEnvThrottle":
    "The endpoint is at its maximum number of environments (scalingConfig.maxEnvironments), all busy.",
  "Platform.EndpointConcurrentInitThrottle":
    "Too many of the endpoint's environments are starting at once. Retry shortly.",
  "Runtime.RequestTimeout": "The application did not answer within the revision's timeout.",
  "Runtime.OutOfMemory": "The application ran out of memory and was stopped.",
  "Runtime.Unreachable": "The application could not be reached on its port.",
  "Runtime.Exit": "The application process exited while handling the request.",
  "Runtime.ExitError": "The application process exited with an error.",
  "Runtime.InvalidResponse":
    "The application's response was not valid HTTP, or its headers were too large.",
  "Runtime.OriginRequestError": "The application closed the connection without a response.",
  "Runtime.ImportCodeError":
    "The package failed to load: the entry point could not be imported. Check the logs for the error.",
  "Runtime.InitTimeout": "The application took too long to start.",
  "Application.InitCodeError": "The application threw while starting up. Check the logs.",
  "Application.TimeoutListenError":
    "The application did not start listening on AWS_LAMBDA_HTTP_ENDPOINT in time.",
  "Overcast.RevisionCodeUnavailable":
    "The revision's package is no longer in S3, so Overcast could not start it.",
}

/** A sentence for `type`, or undefined for one this table does not know. */
export function describeErrorType(type: string): string | undefined {
  return ERROR_TYPES[type]
}

/** Methods an endpoint accepts. */
export const TEST_METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "OPTIONS", "DELETE"] as const
export type TestMethod = (typeof TEST_METHODS)[number]

/** Methods a browser sends no body with. */
export function methodHasBody(method: string): boolean {
  return method !== "GET" && method !== "HEAD"
}

export type BodyKind = "json" | "text" | "binary" | "empty"

export interface DescribedBody {
  kind: BodyKind
  /** The body as shown: pretty-printed JSON, or the text. Empty for binary. */
  text: string
  /** Bytes in the body. */
  size: number
}

/**
 * Says what a response body is, for showing it: JSON pretty-printed, other
 * text as it is, anything else as a size. A body is text when its type says
 * so, or when it decodes as UTF-8 without control characters.
 */
export function describeBody(bytes: Uint8Array, contentType: string | null): DescribedBody {
  const size = bytes.byteLength
  if (size === 0) return { kind: "empty", text: "", size }
  const type = (contentType ?? "").toLowerCase()
  const looksBinary = /^(image|audio|video|font)\//.test(type) || /octet-stream|zip|pdf/.test(type)
  if (looksBinary) return { kind: "binary", text: "", size }
  let text: string
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes)
  } catch {
    return { kind: "binary", text: "", size }
  }
  // eslint-disable-next-line no-control-regex -- control characters are how a binary body is told from text
  if (/[\u0000-\u0008\u000e-\u001f]/.test(text)) return { kind: "binary", text: "", size }
  if (type.includes("json") || /^\s*[[{]/.test(text)) {
    try {
      return { kind: "json", text: JSON.stringify(JSON.parse(text), null, 2), size }
    } catch {
      // not JSON after all
    }
  }
  return { kind: "text", text, size }
}
