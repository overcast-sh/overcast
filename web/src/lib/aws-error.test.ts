import { nullWhen } from "./aws-error"

function awsError(name: string): Error {
  return Object.assign(new Error("boom"), { name })
}

describe("nullWhen", () => {
  it("is the call's result when it succeeds", async () => {
    await expect(nullWhen("NotFoundException", Promise.resolve(1))).resolves.toBe(1)
  })

  it("is null when the service answers with the named code", async () => {
    await expect(
      nullWhen("NotFoundException", Promise.reject(awsError("NotFoundException"))),
    ).resolves.toBeNull()
  })

  it("rethrows any other error", async () => {
    await expect(
      nullWhen("NotFoundException", Promise.reject(awsError("AccessDeniedException"))),
    ).rejects.toThrow("boom")
  })
})
