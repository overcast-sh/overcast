/**
 * Recognising AWS service errors by their wire code.
 *
 * The AWS SDK's deserialiser rebuilds a service error as an Error whose
 * `name` is the error code off the wire — whether or not the client models
 * the exception class — so the name is the one cross-service way to ask what
 * kind of error came back without importing every client's exception types.
 */

/** Did the service answer "that resource does not exist"? */
export function isResourceNotFound(error: unknown): boolean {
  return error instanceof Error && error.name === "ResourceNotFoundException"
}

/**
 * The call's result, or null when the service answered with `code` — the way
 * a Get* for an optional configuration (a policy, a maintenance setting)
 * says there is none.
 */
export async function nullWhen<T>(code: string, call: Promise<T>): Promise<T | null> {
  try {
    return await call
  } catch (error) {
    if (error instanceof Error && error.name === code) return null
    throw error
  }
}
