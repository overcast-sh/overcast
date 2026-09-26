import type { CreateWorkGroupInput, UpdateWorkGroupInput, WorkGroup } from "@aws-sdk/client-athena"

/**
 * The workgroup dialog's fields, and the `CreateWorkGroup` and
 * `UpdateWorkGroup` requests they make. An edit that clears a field sends
 * the matching `Remove…` flag, as the API asks, rather than an empty value.
 */
export interface WorkGroupForm {
  name: string
  description: string
  /** `s3://bucket/prefix/`, or blank for none. */
  outputLocation: string
  enforce: boolean
  /** Bytes scanned cutoff per query, in MB; blank for none. */
  cutoffMb: string
}

const MB = 1024 * 1024

/** AWS's floor for `BytesScannedCutoffPerQuery`: 10 MB. */
export const MIN_CUTOFF_MB = 10

export function workGroupForm(workGroup?: WorkGroup): WorkGroupForm {
  const config = workGroup?.Configuration
  const cutoff = config?.BytesScannedCutoffPerQuery
  return {
    name: workGroup?.Name ?? "",
    description: workGroup?.Description ?? "",
    outputLocation: config?.ResultConfiguration?.OutputLocation ?? "",
    enforce: config?.EnforceWorkGroupConfiguration ?? true,
    cutoffMb: cutoff ? String(Math.round(cutoff / MB)) : "",
  }
}

/** What is wrong with the fields, field by field; empty when they can be sent. */
export function workGroupFormErrors(
  form: WorkGroupForm,
): Partial<Record<keyof WorkGroupForm, string>> {
  const errors: Partial<Record<keyof WorkGroupForm, string>> = {}
  if (!/^[a-zA-Z0-9._-]{1,128}$/.test(form.name)) {
    errors.name = "1–128 letters, digits, '.', '_' or '-'."
  }
  if (form.outputLocation.trim() && !/^s3:\/\/[^/]+/.test(form.outputLocation.trim())) {
    errors.outputLocation = "An S3 location: s3://bucket/prefix/"
  }
  const cutoff = form.cutoffMb.trim()
  if (cutoff && !(Number.isInteger(Number(cutoff)) && Number(cutoff) >= MIN_CUTOFF_MB)) {
    errors.cutoffMb = `A whole number of MB, at least ${MIN_CUTOFF_MB}.`
  }
  return errors
}

function cutoffBytes(form: WorkGroupForm): number | undefined {
  return form.cutoffMb.trim() ? Number(form.cutoffMb) * MB : undefined
}

export function createWorkGroupInput(form: WorkGroupForm): CreateWorkGroupInput {
  const location = form.outputLocation.trim()
  return {
    Name: form.name,
    Description: form.description.trim() || undefined,
    Configuration: {
      ResultConfiguration: location ? { OutputLocation: location } : undefined,
      EnforceWorkGroupConfiguration: form.enforce,
      BytesScannedCutoffPerQuery: cutoffBytes(form),
    },
  }
}

export function updateWorkGroupInput(form: WorkGroupForm): UpdateWorkGroupInput {
  const location = form.outputLocation.trim()
  const cutoff = cutoffBytes(form)
  return {
    WorkGroup: form.name,
    Description: form.description.trim(),
    ConfigurationUpdates: {
      ResultConfigurationUpdates: location
        ? { OutputLocation: location }
        : { RemoveOutputLocation: true },
      EnforceWorkGroupConfiguration: form.enforce,
      ...(cutoff === undefined
        ? { RemoveBytesScannedCutoffPerQuery: true }
        : { BytesScannedCutoffPerQuery: cutoff }),
    },
  }
}
