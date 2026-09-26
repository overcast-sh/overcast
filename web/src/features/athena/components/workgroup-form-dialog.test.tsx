import { render, screen, waitFor } from "@/test/render"
import type * as ApiModule from "@/services/api"
import { WorkGroupFormDialog } from "./workgroup-form-dialog"

const api = vi.hoisted(() => ({
  create: vi.fn((_input: unknown) => Promise.resolve()),
  update: vi.fn((_input: unknown) => Promise.resolve()),
}))

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiModule>()
  return {
    ...actual,
    athena: { ...actual.athena, createWorkGroup: api.create, updateWorkGroup: api.update },
  }
})

beforeEach(() => {
  api.create.mockClear()
  api.update.mockClear()
})

describe("WorkGroupFormDialog > create", () => {
  it("says what is wrong with a name and does not send it", async () => {
    const { user } = render(<WorkGroupFormDialog onClose={() => {}} />)
    await user.type(screen.getByRole("textbox", { name: /Name/ }), "has space{Enter}")
    expect(await screen.findByText("1–128 letters, digits, '.', '_' or '-'.")).toBeInTheDocument()
    expect(api.create).not.toHaveBeenCalled()
  })

  it("creates the workgroup on Enter, the cutoff in bytes", async () => {
    const onClose = vi.fn()
    const { user } = render(<WorkGroupFormDialog onClose={onClose} />)
    await user.type(screen.getByRole("textbox", { name: /Name/ }), "adhoc")
    await user.type(screen.getByRole("textbox", { name: /Data scanned limit/ }), "20{Enter}")
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(api.create.mock.calls[0][0]).toMatchObject({
      Name: "adhoc",
      Configuration: { BytesScannedCutoffPerQuery: 20_000_000 },
    })
  })
})

describe("WorkGroupFormDialog > edit", () => {
  it("sends only what changed, so an unrounded cutoff survives", async () => {
    const onClose = vi.fn()
    const { user } = render(
      <WorkGroupFormDialog
        workGroup={{ Name: "adhoc", Configuration: { BytesScannedCutoffPerQuery: 12_345_678 } }}
        onClose={onClose}
      />,
    )
    await user.type(screen.getByRole("textbox", { name: /Description/ }), "Exploration{Enter}")
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(api.update.mock.calls[0][0]).toEqual({
      WorkGroup: "adhoc",
      Description: "Exploration",
      ConfigurationUpdates: {
        ResultConfigurationUpdates: undefined,
        EnforceWorkGroupConfiguration: undefined,
      },
    })
  })
})
