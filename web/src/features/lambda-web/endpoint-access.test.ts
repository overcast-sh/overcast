import { describe, expect, it } from "vitest"
import {
  curlSigV4Example,
  describeBody,
  describeErrorType,
  domainIdOf,
  errorTypeInBody,
  endpointAddress,
  invokePath,
  invokeRegionOf,
  methodHasBody,
} from "./endpoint-access"

const DOMAIN = "abcd1234abcd1234.b7t.lambda-web.us-east-1.localhost"
const bytes = (text: string) => new TextEncoder().encode(text)

describe("domainIdOf", () => {
  it("is the part before .lambda-web.", () => {
    expect(domainIdOf(DOMAIN)).toBe("abcd1234abcd1234.b7t")
  })

  it("keeps a global MultiRegion id, which has no shard", () => {
    expect(domainIdOf("abcd1234abcd1234.lambda-web.on.aws")).toBe("abcd1234abcd1234")
  })

  it("leaves a name that is not one as it is", () => {
    expect(domainIdOf("example.com")).toBe("example.com")
  })
})

describe("invokeRegionOf", () => {
  it("reads the Region off the domain", () => {
    expect(invokeRegionOf(DOMAIN, "eu-west-1")).toBe("us-east-1")
  })

  it("spells a MultiRegion endpoint's global domain as -", () => {
    expect(invokeRegionOf("abc.lambda-web.global.localhost", "us-east-1")).toBe("-")
  })

  it("falls back to the function's Region when the domain names none", () => {
    expect(invokeRegionOf("abc.b7t.lambda-web.localhost", "eu-west-1")).toBe("eu-west-1")
    expect(invokeRegionOf("not-a-domain", "eu-west-1")).toBe("eu-west-1")
  })
})

describe("invokePath", () => {
  it("is /_overcast/lambdaweb/invoke/{domain-id}/{region}", () => {
    expect(invokePath(DOMAIN, "us-east-1")).toBe(
      "/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1",
    )
  })
})

describe("endpointAddress", () => {
  it("opens the domain, with the port the console reaches the emulator on, where subdomains resolve", () => {
    const domain = "abcd1234abcd1234.b7t.lambda-web.us-east-1.localhost.overcast.sh"
    expect(endpointAddress(domain, "http://localhost.overcast.sh:4570", "us-east-1")).toEqual({
      hostUrl: `http://${domain}:4570/`,
      invokeUrl:
        "http://localhost.overcast.sh:4570/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1/",
    })
  })

  it("keeps the scheme of an https console", () => {
    const domain = "a.b.lambda-web.us-east-1.dev.example.com"
    expect(endpointAddress(domain, "https://dev.example.com", "us-east-1").hostUrl).toBe(
      `https://${domain}/`,
    )
  })

  it.each([
    ["a bare single-label host", "http://localhost:4566"],
    ["an IPv4 address", "http://127.0.0.1:4566"],
    ["an IPv6 address", "http://[::1]:4566"],
  ])("offers only the invoke URL on %s", (_, baseUrl) => {
    const address = endpointAddress(DOMAIN, baseUrl, "us-east-1")
    expect(address.hostUrl).toBeUndefined()
    expect(address.invokeUrl).toBe(
      `${baseUrl}/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1/`,
    )
  })

  it("offers only the invoke URL for a domain minted on some other host", () => {
    const address = endpointAddress(
      "abc.b7t.lambda-web.us-east-1.on.aws",
      "http://dev.example.com:4566",
      "us-east-1",
    )
    expect(address.hostUrl).toBeUndefined()
  })

  it("tolerates a base URL with a trailing slash", () => {
    expect(endpointAddress(DOMAIN, "http://localhost:4566/", "us-east-1").invokeUrl).toBe(
      "http://localhost:4566/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1/",
    )
  })
})

describe("curlSigV4Example", () => {
  it("signs for service lambda in the endpoint's Region and sends the payload hash header", () => {
    const curl = curlSigV4Example("http://x/", "eu-west-1")
    expect(curl).toContain('--aws-sigv4 "aws:amz:eu-west-1:lambda"')
    expect(curl).toContain("x-amz-content-sha256")
    expect(curl).toContain('"http://x/"')
  })
})

describe("describeErrorType", () => {
  it.each([
    ["Platform.EndpointRequestThrottle", /rate limit/],
    ["Runtime.ImportCodeError", /failed to load/],
    ["Runtime.RequestTimeout", /timeout/],
    ["Platform.RevisionNotActive", /not Active/],
  ])("explains %s", (type, words) => {
    expect(describeErrorType(type)).toMatch(words)
  })

  it("has nothing to say about a type it does not know", () => {
    expect(describeErrorType("Custom.Thing")).toBeUndefined()
  })
})

describe("methodHasBody", () => {
  it("is false for the methods a browser sends no body with", () => {
    expect(["GET", "HEAD"].map(methodHasBody)).toEqual([false, false])
    expect(["POST", "PUT", "PATCH", "DELETE", "OPTIONS"].every(methodHasBody)).toBe(true)
  })
})

describe("describeBody", () => {
  it("says an empty body is empty", () => {
    expect(describeBody(new Uint8Array(), null)).toEqual({ kind: "empty", text: "", size: 0 })
  })

  it("pretty-prints JSON", () => {
    const body = describeBody(bytes('{"a":1,"b":[2]}'), "application/json")
    expect(body.kind).toBe("json")
    expect(body.text).toBe('{\n  "a": 1,\n  "b": [\n    2\n  ]\n}')
  })

  it("recognises JSON a server did not label as such", () => {
    expect(describeBody(bytes("[1]"), "text/plain").kind).toBe("json")
  })

  it("shows text as it is, including text that only looks like JSON", () => {
    expect(describeBody(bytes("hello"), "text/plain")).toEqual({
      kind: "text",
      text: "hello",
      size: 5,
    })
    expect(describeBody(bytes("{not json"), "text/plain").kind).toBe("text")
  })

  it("shows a binary body as its size", () => {
    expect(describeBody(new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0, 1]), "image/png")).toEqual({
      kind: "binary",
      text: "",
      size: 6,
    })
    // Not labelled, but not text either.
    expect(describeBody(new Uint8Array([0xff, 0xfe, 0x00, 0x01]), null).kind).toBe("binary")
  })
})

describe("errorTypeInBody", () => {
  it("finds the type a start failure names", () => {
    expect(errorTypeInBody("The application failed to start: Application.InitCodeError boom")).toBe(
      "Application.InitCodeError",
    )
  })

  it("ignores something that only looks like a type", () => {
    expect(errorTypeInBody("see Runtime.Nonsense and Foo.Bar")).toBeUndefined()
    expect(errorTypeInBody("")).toBeUndefined()
  })
})
