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
  it("reads a workgroup's configuration, the cutoff in MB", () => {
    expect(
      workGroupForm({
        Name: "adhoc",
        Configuration: {
          ResultConfiguration: { OutputLocation: "s3://r/" },
          EnforceWorkGroupConfiguration: false,
          BytesScannedCutoffPerQuery: 100 * 1024 * 1024,
        },
      }),
    ).toEqual({
      name: "adhoc",
      description: "",
      outputLocation: "s3://r/",
      enforce: false,
      cutoffMb: "100",
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
        BytesScannedCutoffPerQuery: 100 * 1024 * 1024,
      },
    })
  })
})

describe("updateWorkGroupInput", () => {
  it("removes a cleared location and cutoff rather than sending them empty", () => {
    const input = updateWorkGroupInput({ ...filled, outputLocation: "", cutoffMb: "" })
    expect(input.ConfigurationUpdates).toEqual({
      ResultConfigurationUpdates: { RemoveOutputLocation: true },
      EnforceWorkGroupConfiguration: true,
      RemoveBytesScannedCutoffPerQuery: true,
    })
  })
})
