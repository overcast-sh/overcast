import { describe, expect, it } from "vitest"
import type { FunctionEndpointSummary } from "@aws-sdk/client-lambda-web"
import {
  RATE_LIMITS,
  defaultLogGroup,
  describeServes,
  emptyEndpointDraft,
  emptyRevisionDraft,
  emptyWeights,
  endpointDraftOf,
  endpointProblems,
  endpointsServing,
  isTransitionalEndpoint,
  isTransitionalState,
  nameProblem,
  revisionProblems,
  systemLogGroup,
  toCreateEndpoint,
  toLimits,
  toRevisionConfig,
  toRevisionWeights,
  toUpdateEndpoint,
  weightsDraftOf,
  weightsProblem,
  type RevisionDraft,
} from "./models"

const validRevision: RevisionDraft = {
  ...emptyRevisionDraft(),
  bucket: "code-bucket",
  key: "app.zip",
  executionRoleArn: "arn:aws:iam::000000000000:role/web-role",
}

describe("nameProblem", () => {
  it.each(["shop", "my-web_app", "a", "A1"])("accepts %s", (name) => {
    expect(nameProblem(name)).toBeUndefined()
  })

  it.each(["-shop", "shop-", "_shop", "my app", "shop!", "x".repeat(65)])("refuses %s", (name) => {
    expect(nameProblem(name)).toBeDefined()
  })
})

describe("revisionProblems", () => {
  it("needs a bucket, a key and a role ARN", () => {
    expect(Object.keys(revisionProblems(emptyRevisionDraft())).sort()).toEqual([
      "bucket",
      "executionRoleArn",
      "key",
    ])
    expect(revisionProblems(validRevision)).toEqual({})
  })

  it("holds the timeout to 3-900 and the concurrency to 1-128, and lets both be blank", () => {
    expect(revisionProblems({ ...validRevision, timeoutSeconds: "2" }).timeoutSeconds).toBeDefined()
    expect(
      revisionProblems({ ...validRevision, timeoutSeconds: "901" }).timeoutSeconds,
    ).toBeDefined()
    expect(revisionProblems({ ...validRevision, timeoutSeconds: "3" })).toEqual({})
    expect(revisionProblems({ ...validRevision, timeoutSeconds: "900" })).toEqual({})
    expect(revisionProblems({ ...validRevision, maxConcurrency: "0" }).maxConcurrency).toBeDefined()
    expect(
      revisionProblems({ ...validRevision, maxConcurrency: "129" }).maxConcurrency,
    ).toBeDefined()
    expect(
      revisionProblems({ ...validRevision, maxConcurrency: "1.5" }).maxConcurrency,
    ).toBeDefined()
    expect(revisionProblems({ ...validRevision, maxConcurrency: "128" })).toEqual({})
  })
})

describe("toRevisionConfig", () => {
  it("sends only what was filled in", () => {
    expect(toRevisionConfig(validRevision)).toEqual({
      buildConfig: {
        codeConfig: { s3Object: { bucket: "code-bucket", key: "app.zip" } },
        runtimeConfig: { runtime: "nodejs24.x" },
      },
      serviceConfig: { executionRoleArn: "arn:aws:iam::000000000000:role/web-role" },
    })
  })

  it("carries the optional members, dropping variables with no name", () => {
    const config = toRevisionConfig({
      ...validRevision,
      versionId: " v3 ",
      description: " nightly ",
      timeoutSeconds: "60",
      maxConcurrency: "8",
      env: [
        { key: "NODE_ENV", value: "production" },
        { key: "  ", value: "ignored" },
      ],
    })
    expect(config.description).toBe("nightly")
    expect(config.buildConfig?.codeConfig?.s3Object?.versionId).toBe("v3")
    expect(config.serviceConfig).toMatchObject({
      timeoutSeconds: 60,
      maxConcurrencyPerEnvironment: 8,
      environmentVariables: { NODE_ENV: "production" },
    })
  })
})

describe("traffic weights", () => {
  it("is nothing without a revision, all to one, or the split between two", () => {
    expect(toRevisionWeights(emptyWeights())).toEqual([])
    expect(toRevisionWeights({ primary: "r1", secondary: "", primaryWeight: 90 })).toEqual([
      { revisionId: "r1", weight: 100 },
    ])
    expect(toRevisionWeights({ primary: "r1", secondary: "r2", primaryWeight: 90 })).toEqual([
      { revisionId: "r1", weight: 90 },
      { revisionId: "r2", weight: 10 },
    ])
  })

  it("always totals 100", () => {
    for (let share = 1; share <= 99; share++) {
      const total = toRevisionWeights({
        primary: "a",
        secondary: "b",
        primaryWeight: share,
      }).reduce((sum, w) => sum + (w.weight ?? 0), 0)
      expect(total).toBe(100)
    }
  })

  it("reads an endpoint's weights back into a draft", () => {
    expect(weightsDraftOf([{ revisionId: "r1", weight: 100 }])).toMatchObject({
      primary: "r1",
      secondary: "",
    })
    expect(
      weightsDraftOf([
        { revisionId: "r1", weight: 70 },
        { revisionId: "r2", weight: 30 },
      ]),
    ).toEqual({ primary: "r1", secondary: "r2", primaryWeight: 70 })
    expect(weightsDraftOf(undefined)).toEqual(emptyWeights())
  })

  it("wants a revision, and two different ones to split", () => {
    expect(weightsProblem(emptyWeights())).toBeDefined()
    expect(weightsProblem({ primary: "r1", secondary: "r1", primaryWeight: 50 })).toBeDefined()
    expect(weightsProblem({ primary: "r1", secondary: "r2", primaryWeight: 50 })).toBeUndefined()
  })
})

describe("RATE_LIMITS", () => {
  it("is 0, 100 to 1000 by 100, then 2000 to 10000 by 1000", () => {
    expect(RATE_LIMITS).toEqual([
      0, 100, 200, 300, 400, 500, 600, 700, 800, 900, 1000, 2000, 3000, 4000, 5000, 6000, 7000,
      8000, 9000, 10000,
    ])
  })
})

describe("toLimits", () => {
  it("sends the limits that are set", () => {
    expect(toLimits({ maxEnvironments: "20", rateLimit: "500" })).toEqual({
      scalingConfig: { maxEnvironments: 20 },
      throttleConfig: { rateLimit: 500 },
    })
  })

  it("sends a rate limit of zero: it is a limit", () => {
    expect(toLimits({ maxEnvironments: "", rateLimit: "0" })).toEqual({
      throttleConfig: { rateLimit: 0 },
    })
  })

  it("sends nothing for a limit that was never set", () => {
    expect(toLimits({ maxEnvironments: "", rateLimit: "" })).toEqual({})
  })

  it("sends the empty object that clears a limit the endpoint had", () => {
    expect(
      toLimits(
        { maxEnvironments: "", rateLimit: "" },
        { scaling: { maxEnvironments: 5 }, throttle: { rateLimit: 100 } },
      ),
    ).toEqual({ scalingConfig: {}, throttleConfig: {} })
  })
})

describe("endpointProblems", () => {
  const draft = { ...emptyEndpointDraft(), name: "prod" }

  it("needs a name the function does not already use", () => {
    expect(endpointProblems(emptyEndpointDraft(), { creating: true }).name).toBeDefined()
    expect(endpointProblems(draft, { creating: true, taken: ["prod"] }).name).toBeDefined()
    expect(endpointProblems(draft, { creating: true, taken: ["dev"] })).toEqual({})
  })

  it("needs a Region and weights for a MultiRegion or PerRegion endpoint", () => {
    const multi = { ...draft, type: "MultiRegion" as const, deployment: "Disabled" as const }
    expect(Object.keys(endpointProblems(multi, { creating: true })).sort()).toEqual([
      "regions",
      "weights",
    ])
    expect(
      endpointProblems(
        {
          ...multi,
          regions: ["us-west-2"],
          weights: { primary: "r1", secondary: "", primaryWeight: 90 },
        },
        { creating: true },
      ),
    ).toEqual({})
  })

  it("lets a HomeRegion endpoint be created with no weights", () => {
    expect(endpointProblems({ ...draft, deployment: "Disabled" }, { creating: true })).toEqual({})
  })

  it("holds max environments to 2-10000", () => {
    const withMax = (maxEnvironments: string) => ({
      ...draft,
      limits: { maxEnvironments, rateLimit: "" },
    })
    expect(endpointProblems(withMax("1"), { creating: true }).maxEnvironments).toBeDefined()
    expect(endpointProblems(withMax("10001"), { creating: true }).maxEnvironments).toBeDefined()
    expect(endpointProblems(withMax("2"), { creating: true })).toEqual({})
  })
})

describe("toCreateEndpoint", () => {
  it("leaves the weights to LatestRevision, and the Regions to a HomeRegion endpoint", () => {
    const request = toCreateEndpoint("shop", {
      ...emptyEndpointDraft(),
      name: "dev",
      weights: { primary: "r1", secondary: "", primaryWeight: 90 },
    })
    expect(request).toEqual({
      functionName: "shop",
      endpointName: "dev",
      endpointType: "HomeRegion",
      authType: "ApplicationManaged",
      autoDeploymentMode: "LatestRevision",
    })
  })

  it("sends the weights and Regions of a pinned PerRegion endpoint", () => {
    const request = toCreateEndpoint("shop", {
      ...emptyEndpointDraft(),
      name: "eu",
      type: "PerRegion",
      deployment: "Disabled",
      regions: ["eu-west-1"],
      weights: { primary: "r1", secondary: "r2", primaryWeight: 80 },
    })
    expect(request).toMatchObject({
      endpointType: "PerRegion",
      autoDeploymentMode: "Disabled",
      regions: ["eu-west-1"],
      revisionWeights: [
        { revisionId: "r1", weight: 80 },
        { revisionId: "r2", weight: 20 },
      ],
    })
  })
})

describe("toUpdateEndpoint", () => {
  const before: FunctionEndpointSummary = {
    endpointName: "prod",
    description: "old",
    endpointType: "HomeRegion",
    authType: "ApplicationManaged",
    autoDeploymentMode: "Disabled",
    revisionWeights: [{ revisionId: "r1", weight: 100 }],
    scalingConfig: { maxEnvironments: 10 },
    domainName: "d",
    regions: [],
    state: "Active",
    stateReason: "",
    endpointArn: "arn",
    createdAt: undefined,
    updatedAt: undefined,
  }

  it("sends the weights only when they changed", () => {
    const same = toUpdateEndpoint("shop", before, endpointDraftOf(before))
    expect(same).not.toHaveProperty("revisionWeights")

    const draft = endpointDraftOf(before)
    const split = toUpdateEndpoint("shop", before, {
      ...draft,
      weights: { primary: "r1", secondary: "r2", primaryWeight: 90 },
    })
    expect(split.revisionWeights).toEqual([
      { revisionId: "r1", weight: 90 },
      { revisionId: "r2", weight: 10 },
    ])
  })

  it("never sends weights beside LatestRevision", () => {
    const draft = endpointDraftOf(before)
    const request = toUpdateEndpoint("shop", before, {
      ...draft,
      deployment: "LatestRevision",
      weights: { primary: "r1", secondary: "r2", primaryWeight: 90 },
    })
    expect(request.autoDeploymentMode).toBe("LatestRevision")
    expect(request).not.toHaveProperty("revisionWeights")
  })

  it("clears a limit that was emptied, and an empty description", () => {
    const draft = endpointDraftOf(before)
    const request = toUpdateEndpoint("shop", before, {
      ...draft,
      description: "",
      limits: { maxEnvironments: "", rateLimit: "" },
    })
    expect(request).toMatchObject({ description: "", scalingConfig: {} })
    expect(request).not.toHaveProperty("throttleConfig")
  })
})

describe("endpointsServing", () => {
  it("names the endpoints whose traffic includes the revision", () => {
    const endpoints = [
      {
        endpointName: "a",
        revisionWeights: [
          { revisionId: "r1", weight: 90 },
          { revisionId: "r2", weight: 10 },
        ],
      },
      { endpointName: "b", revisionWeights: [{ revisionId: "r2", weight: 100 }] },
      { endpointName: "c", revisionWeights: [] },
    ]
    expect(endpointsServing("r1", endpoints)).toEqual(["a"])
    expect(endpointsServing("r2", endpoints)).toEqual(["a", "b"])
    expect(endpointsServing("r3", endpoints)).toEqual([])
  })
})

describe("describeServes", () => {
  it("reads as the map does", () => {
    expect(
      describeServes([
        { revisionId: "rev-ab12c", weight: 90 },
        { revisionId: "rev-de34f", weight: 10 },
      ]),
    ).toBe("rev-ab12c 90% · rev-de34f 10%")
    expect(describeServes([])).toBe("no revision")
    expect(describeServes(undefined)).toBe("no revision")
  })
})

describe("logging configuration", () => {
  const filled = {
    ...emptyRevisionDraft(),
    bucket: "code",
    key: "app.zip",
    executionRoleArn: "arn:aws:iam::000000000000:role/web-role",
  }

  it("names the log groups: application logs, and the .local twin for system logs", () => {
    expect(defaultLogGroup("shop")).toBe("/aws/lambda/web/shop")
    expect(systemLogGroup("/aws/lambda/web/shop")).toBe("/aws/lambda/web/shop.local")
  })

  it("sends no telemetryConfig when nothing was chosen", () => {
    expect(toRevisionConfig(filled).serviceConfig?.telemetryConfig).toBeUndefined()
  })

  it("sends only the members that were chosen", () => {
    expect(
      toRevisionConfig({ ...filled, applicationLogLevel: "ERROR" }).serviceConfig?.telemetryConfig,
    ).toEqual({ loggingConfig: { applicationLogLevel: "ERROR" } })
    expect(
      toRevisionConfig({ ...filled, logGroup: " /custom/shop ", systemLogLevel: "DEBUG" })
        .serviceConfig?.telemetryConfig,
    ).toEqual({ loggingConfig: { logGroup: "/custom/shop", systemLogLevel: "DEBUG" } })
  })

  it("refuses a log group CloudWatch Logs would not accept", () => {
    expect(revisionProblems({ ...filled, logGroup: "has spaces" }).logGroup).toBeDefined()
    expect(
      revisionProblems({ ...filled, logGroup: "/ok/group.name-1_2#x" }).logGroup,
    ).toBeUndefined()
    expect(revisionProblems({ ...filled, logGroup: "" }).logGroup).toBeUndefined()
  })
})

describe("transitional states", () => {
  it.each([
    ["Pending", true],
    ["Deleting", true],
    ["Active", false],
    ["Failed", false],
    [undefined, false],
  ])("%s is transitional: %s", (state, expected) => {
    expect(isTransitionalState(state)).toBe(expected)
  })

  it("keeps polling an endpoint whose update is still in progress", () => {
    expect(isTransitionalEndpoint({ state: "Active", updateStatus: "InProgress" })).toBe(true)
    expect(isTransitionalEndpoint({ state: "Active", updateStatus: "Successful" })).toBe(false)
    expect(isTransitionalEndpoint({ state: "Pending" })).toBe(true)
  })
})
