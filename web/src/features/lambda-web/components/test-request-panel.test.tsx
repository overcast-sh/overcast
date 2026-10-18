import type { ComponentProps } from "react"
import { http, HttpResponse } from "msw"
import { server } from "@/test/server"
import { render, screen, within } from "@/test/render"
import { TestRequestPanel } from "./test-request-panel"

const DOMAIN = "abcd1234abcd1234.b7t.lambda-web.us-east-1.localhost"
const INVOKE = "http://localhost:4566/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1"

/** Answers the invoke path, keeping what the request looked like. */
function answer(respond: () => Response) {
  const seen: { method?: string; url?: URL; headers?: Headers; body?: string } = {}
  server.use(
    http.all(`${INVOKE}/*`, async ({ request }) => {
      seen.method = request.method
      seen.url = new URL(request.url)
      seen.headers = request.headers
      seen.body = await request.text()
      return respond()
    }),
  )
  return seen
}

function renderPanel(props: Partial<ComponentProps<typeof TestRequestPanel>> = {}) {
  return render(
    <TestRequestPanel
      domainName={DOMAIN}
      region="us-east-1"
      authType="ApplicationManaged"
      environments={1}
      {...props}
    />,
  )
}

describe("TestRequestPanel", () => {
  it("sends a GET through the invoke path and shows the status, headers, timing and JSON body", async () => {
    const seen = answer(() =>
      HttpResponse.json(
        { hello: "world" },
        {
          headers: {
            "lambda-web-request-id": "REQ123",
            "lambda-web-logstream": "2026/10/18/[1]abc",
          },
        },
      ),
    )
    const { user } = renderPanel()
    await user.clear(screen.getByLabelText("Path and query"))
    await user.type(screen.getByLabelText("Path and query"), "/hello?name=overcast")
    await user.click(screen.getByRole("button", { name: "Send request" }))

    const response = await screen.findByRole("group", { name: "Response" })
    expect(within(response).getByText(/^200/)).toBeInTheDocument()
    expect(within(response).getByText(/^\d+ ms$/)).toBeInTheDocument()
    expect(within(response).getByText("REQ123")).toBeInTheDocument()
    expect(within(response).getByText("2026/10/18/[1]abc")).toBeInTheDocument()
    // Pretty-printed.
    expect(within(response).getByText(/"hello": "world"/)).toBeInTheDocument()
    expect(seen.method).toBe("GET")
    expect(seen.url?.pathname).toBe(
      "/_overcast/lambdaweb/invoke/abcd1234abcd1234.b7t/us-east-1/hello",
    )
    expect(seen.url?.search).toBe("?name=overcast")
    expect(within(response).queryByText(/cold start/i)).not.toBeInTheDocument()
  })

  it("sends the method, headers and body a person entered", async () => {
    const seen = answer(() => new HttpResponse("ok", { status: 201 }))
    const { user } = renderPanel()
    await user.selectOptions(screen.getByLabelText("Method"), "POST")
    await user.click(screen.getByRole("button", { name: "Add header" }))
    await user.type(screen.getByLabelText("request header 1 name"), "x-trace")
    await user.type(screen.getByLabelText("request header 1 value"), "abc")
    await user.type(screen.getByLabelText("Body"), "payload")
    await user.click(screen.getByRole("button", { name: "Send request" }))

    expect(await screen.findByText(/^201/)).toBeInTheDocument()
    expect(seen.method).toBe("POST")
    expect(seen.headers?.get("x-trace")).toBe("abc")
    expect(seen.body).toBe("payload")
  })

  it("offers no body for the methods that send none", async () => {
    const { user } = renderPanel()
    expect(screen.queryByLabelText("Body")).not.toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText("Method"), "PUT")
    expect(screen.getByLabelText("Body")).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText("Method"), "HEAD")
    expect(screen.queryByLabelText("Body")).not.toBeInTheDocument()
  })

  it("explains a platform error in plain English", async () => {
    answer(
      () =>
        new HttpResponse("The application failed to start: Runtime.ImportCodeError", {
          status: 502,
          headers: {
            "lambda-web-request-id": "REQ9",
            "lambda-web-error-type": "Runtime.ImportCodeError",
          },
        }),
    )
    const { user } = renderPanel()
    await user.click(screen.getByRole("button", { name: "Send request" }))

    const response = await screen.findByRole("group", { name: "Response" })
    expect(within(response).getByText(/^502/)).toBeInTheDocument()
    expect(within(response).getByText("Runtime.ImportCodeError")).toBeInTheDocument()
    expect(within(response).getByText(/package failed to load/)).toBeInTheDocument()
    expect(within(response).getByText(/failed to start/)).toBeInTheDocument()
  })

  it("explains a throttle", async () => {
    answer(
      () =>
        new HttpResponse("The endpoint request rate limit was exceeded.", {
          status: 429,
          headers: { "lambda-web-error-type": "Platform.EndpointRequestThrottle" },
        }),
    )
    const { user } = renderPanel()
    await user.click(screen.getByRole("button", { name: "Send request" }))
    expect(await screen.findByText(/^429/)).toBeInTheDocument()
    expect(screen.getByText(/rate limit \(throttleConfig.rateLimit\)/)).toBeInTheDocument()
  })

  it("marks a request sent while the endpoint had no environments as a likely cold start", async () => {
    answer(() => new HttpResponse("hi"))
    const { user } = renderPanel({ environments: 0 })
    expect(
      screen.getByText(/Scaled to zero: this request starts an environment/),
    ).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Send request" }))
    expect(await screen.findByText("Likely a cold start")).toBeInTheDocument()
  })

  it("does not call a request cold when the environment count is not known", async () => {
    answer(() => new HttpResponse("hi"))
    const { user } = renderPanel({ environments: undefined })
    await user.click(screen.getByRole("button", { name: "Send request" }))
    await screen.findByRole("group", { name: "Response" })
    expect(screen.queryByText("Likely a cold start")).not.toBeInTheDocument()
  })

  it("shows a binary body as its size", async () => {
    answer(
      () =>
        new HttpResponse(new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x00, 0x01]), {
          headers: { "content-type": "image/png" },
        }),
    )
    const { user } = renderPanel()
    await user.click(screen.getByRole("button", { name: "Send request" }))
    expect(await screen.findByText(/binary, 6 B/)).toBeInTheDocument()
  })

  it("says so when the browser cannot read the lambda-web headers", async () => {
    answer(() => new HttpResponse("hi"))
    const { user } = renderPanel()
    await user.click(screen.getByRole("button", { name: "Send request" }))
    expect(await screen.findByText(/cannot read the/)).toBeInTheDocument()
  })

  it("reports a request that never reached the emulator", async () => {
    server.use(http.all(`${INVOKE}/*`, () => HttpResponse.error()))
    const { user } = renderPanel()
    await user.click(screen.getByRole("button", { name: "Send request" }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/did not reach the emulator/)
  })

  it("is off for an IAM auth endpoint, and says why", () => {
    renderPanel({ authType: "IamAuth" })
    expect(
      screen.getByText("IAM auth endpoints cannot be tested from the console"),
    ).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Send request" })).not.toBeInTheDocument()
  })
})

describe("TestRequestPanel without readable headers", () => {
  it("takes the error type from the body when the browser hides the header", async () => {
    server.use(
      http.all(`${INVOKE}/*`, () =>
        HttpResponse.text("The application failed to start: Runtime.ImportCodeError\nboom", {
          status: 502,
        }),
      ),
    )
    const { user } = renderPanel()
    await user.click(screen.getByRole("button", { name: "Send request" }))
    expect(await screen.findByText("Runtime.ImportCodeError")).toBeInTheDocument()
    expect(screen.getByText(/package failed to load/)).toBeInTheDocument()
  })
})
