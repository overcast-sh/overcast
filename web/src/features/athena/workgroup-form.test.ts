import {
  createWorkGroupInput,
  updateWorkGroupInput,
  workGroupForm,
  workGroupFormErrors,
} from "./workgroup-form"

const filled = {
  name: "adhoc",
  description: " Ad hoc queries ",
  outputLocation: "s3://results/adhoc/",
  enforce: true,
  cutoffMb: "100",
}

describe("workGroupForm", () => {
  it("reads a workgroup's configuration, the cutoff in decimal MB", () => {
    expect(
      workGroupForm({
        Name: "adhoc",
        Configuration: {
          ResultConfiguration: { OutputLocation: "s3://r/" },
          EnforceWorkGroupConfiguration: false,
          BytesScannedCutoffPerQuery: 10_000_000,
        },
      }),
    ).toEqual({
      name: "adhoc",
      description: "",
      outputLocation: "s3://r/",
      enforce: false,
      cutoffMb: "10",
    })
  })
})

describe("workGroupFormErrors", () => {
  it("accepts a filled form", () => {
    expect(workGroupFormErrors(filled)).toEqual({})
  })

  it.each([
    ["name", { name: "has space" }],
    ["outputLocation", { outputLocation: "results/adhoc" }],
    ["cutoffMb", { cutoffMb: "5" }],
  ])("rejects a bad %s", (field, patch) => {
    expect(workGroupFormErrors({ ...filled, ...patch })).toHaveProperty(field)
  })
})

describe("createWorkGroupInput", () => {
  it("sends the configuration, the cutoff in bytes", () => {
    expect(createWorkGroupInput(filled)).toEqual({
      Name: "adhoc",
      Description: "Ad hoc queries",
      Configuration: {
        ResultConfiguration: { OutputLocation: "s3://results/adhoc/" },
        EnforceWorkGroupConfiguration: true,
        BytesScannedCutoffPerQuery: 100_000_000,
      },
    })
  })
})

describe("updateWorkGroupInput", () => {
  it("sends nothing for fields left as they were", () => {
    const original = workGroupForm({
      Name: "adhoc",
      Configuration: { BytesScannedCutoffPerQuery: 12_345_678 },
    })
    const input = updateWorkGroupInput(original, { ...original, description: "New" })
    expect(input).toEqual({
      WorkGroup: "adhoc",
      Description: "New",
      ConfigurationUpdates: {
        ResultConfigurationUpdates: undefined,
        EnforceWorkGroupConfiguration: undefined,
      },
    })
  })

  it("removes a cleared location and cutoff rather than sending them empty", () => {
    const input = updateWorkGroupInput(filled, { ...filled, outputLocation: "", cutoffMb: "" })
    expect(input.ConfigurationUpdates).toMatchObject({
      ResultConfigurationUpdates: { RemoveOutputLocation: true },
      RemoveBytesScannedCutoffPerQuery: true,
    })
  })

  it("sends a changed cutoff in bytes", () => {
    const input = updateWorkGroupInput(filled, { ...filled, cutoffMb: "25" })
    expect(input.ConfigurationUpdates?.BytesScannedCutoffPerQuery).toBe(25_000_000)
  })
})
