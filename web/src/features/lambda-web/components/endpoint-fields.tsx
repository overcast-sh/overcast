import { Check } from "lucide-react"
import { Combobox, type ComboboxRenderContext } from "@/components/ui/combobox"
import { FormField, FormRow } from "@/components/ui/form"
import { Input } from "@/components/ui/input"
import { AWS_REGIONS } from "@/components/ui/region-select"
import { SegmentedControl } from "@/components/ui/segmented-control"
import { Select } from "@/components/ui/select"
import { cn } from "@/lib/utils"
import type { WebRevision } from "@/services/api/lambda-web"
import {
  AUTH_OPTIONS,
  DEPLOYMENT_OPTIONS,
  ENDPOINT_TYPES,
  MAX_ENVIRONMENTS_RANGE,
  RATE_LIMITS,
  type EndpointDraft,
  type EndpointProblems,
  type WeightsDraft,
} from "../models"

interface EndpointFieldsProps {
  draft: EndpointDraft
  onChange: (draft: EndpointDraft) => void
  problems: EndpointProblems
  /** A new endpoint also chooses its name, type and Regions; an existing one cannot change them. */
  creating: boolean
  /** The function's home Region, which a Multi- or PerRegion endpoint always includes. */
  homeRegion: string
  revisions: WebRevision[]
}

function renderRegion(region: (typeof AWS_REGIONS)[number], { selected }: ComboboxRenderContext) {
  return (
    <span className="flex items-center gap-2">
      <Check className={cn("size-3.5 shrink-0", selected ? "opacity-100" : "opacity-0")} />
      <span className="font-mono text-sm">{region.code}</span>
      <span className="ml-auto text-sm text-fg-muted">{region.name}</span>
    </span>
  )
}

/**
 * The fields of an endpoint, shared by the create and edit dialogs: a create
 * adds the name, type and Regions an update cannot change. Traffic weights
 * appear only for a pinned endpoint — `LatestRevision` chooses the revision
 * itself, and the service refuses weights alongside it.
 */
export function EndpointFields({
  draft,
  onChange,
  problems,
  creating,
  homeRegion,
  revisions,
}: EndpointFieldsProps) {
  const set = (patch: Partial<EndpointDraft>) => onChange({ ...draft, ...patch })
  const homeOnly = draft.type !== "HomeRegion"
  return (
    <>
      {creating && (
        <>
          <FormField label="Endpoint name" required error={problems.name}>
            <Input
              autoFocus
              value={draft.name}
              placeholder="production"
              onChange={(event) => set({ name: event.target.value })}
            />
          </FormField>
          <FormField label="Type">
            <Select
              value={draft.type}
              onChange={(event) => {
                const type = event.target.value as EndpointDraft["type"]
                // LatestRevision is a HomeRegion feature: leaving it pins the endpoint.
                set({
                  type,
                  deployment: type === "HomeRegion" ? draft.deployment : "Disabled",
                  regions: type === "HomeRegion" ? [] : draft.regions,
                })
              }}
            >
              {ENDPOINT_TYPES.map((type) => (
                <option key={type} value={type}>
                  {type}
                </option>
              ))}
            </Select>
          </FormField>
          {homeOnly && (
            <FormField
              label="Other Regions"
              required
              error={problems.regions}
              hint={`${homeRegion} is always included. Nothing runs in the other Regions; they are recorded.`}
            >
              <Combobox
                multiple
                aria-label="Other Regions"
                value={draft.regions}
                onChange={(regions) => set({ regions })}
                items={AWS_REGIONS.filter((r) => r.code !== homeRegion)}
                filterFn={(r, q) =>
                  r.code.includes(q) || r.name.toLowerCase().includes(q.toLowerCase())
                }
                getItemValue={(r) => r.code}
                renderItem={renderRegion}
                placeholder="Add a Region"
                inputClassName="font-mono"
              />
            </FormField>
          )}
        </>
      )}
      <FormField label="Description">
        <Input
          value={draft.description}
          maxLength={256}
          onChange={(event) => set({ description: event.target.value })}
        />
      </FormField>
      <FormField label="Authentication">
        <SegmentedControl
          label="Authentication"
          size="md"
          value={draft.authType}
          options={AUTH_OPTIONS}
          onChange={(authType) => set({ authType })}
        />
      </FormField>
      <FormField
        label="Deployment"
        hint={
          homeOnly
            ? "Latest-revision routing is available for HomeRegion endpoints only."
            : undefined
        }
      >
        <SegmentedControl
          label="Deployment"
          size="md"
          value={draft.deployment}
          options={
            homeOnly ? DEPLOYMENT_OPTIONS.filter((o) => o.value === "Disabled") : DEPLOYMENT_OPTIONS
          }
          onChange={(deployment) => set({ deployment })}
        />
      </FormField>
      {draft.deployment === "Disabled" && (
        <WeightsFields
          weights={draft.weights}
          onChange={(weights) => set({ weights })}
          revisions={revisions}
          problem={problems.weights}
          optional={!creating || !homeOnly}
        />
      )}
      <FormRow>
        <FormField
          label="Max environments"
          error={problems.maxEnvironments}
          hint={`${MAX_ENVIRONMENTS_RANGE.min} to ${MAX_ENVIRONMENTS_RANGE.max}. Blank for no cap.`}
        >
          <Input
            inputMode="numeric"
            value={draft.limits.maxEnvironments}
            onChange={(event) =>
              set({ limits: { ...draft.limits, maxEnvironments: event.target.value } })
            }
          />
        </FormField>
        <FormField label="Rate limit (requests/s)" hint="Blank for no limit.">
          <Select
            value={draft.limits.rateLimit}
            onChange={(event) =>
              set({ limits: { ...draft.limits, rateLimit: event.target.value } })
            }
          >
            <option value="">No limit</option>
            {RATE_LIMITS.map((rate) => (
              <option key={rate} value={rate}>
                {rate}
              </option>
            ))}
          </Select>
        </FormField>
      </FormRow>
    </>
  )
}

const SPLIT_PRESETS = [95, 90, 50] as const

interface WeightsFieldsProps {
  weights: WeightsDraft
  onChange: (weights: WeightsDraft) => void
  revisions: WebRevision[]
  problem?: string
  /** True when the endpoint can be saved with no revision chosen. */
  optional: boolean
}

/**
 * Which revision serves the endpoint, or which two share it: the canary split
 * is the primary's share against the other's, always totalling 100.
 */
function WeightsFields({ weights, onChange, revisions, problem, optional }: WeightsFieldsProps) {
  const set = (patch: Partial<WeightsDraft>) => onChange({ ...weights, ...patch })
  const splitting = weights.secondary !== ""
  const options = revisions.map((r) => r.revisionId ?? "")
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3">
      <FormField
        label="Serves revision"
        required={!optional}
        error={problem}
        hint={optional ? "Optional. An endpoint with no revision serves nothing." : undefined}
      >
        <Select value={weights.primary} onChange={(event) => set({ primary: event.target.value })}>
          <option value="">
            {options.length === 0 ? "No revisions yet" : "Choose a revision"}
          </option>
          {options.map((id) => (
            <option key={id} value={id}>
              {id}
            </option>
          ))}
        </Select>
      </FormField>
      {weights.primary !== "" && (
        <FormField label="Split traffic with">
          <Select
            value={weights.secondary}
            onChange={(event) => set({ secondary: event.target.value })}
          >
            <option value="">Nobody — all traffic to one revision</option>
            {options
              .filter((id) => id !== weights.primary)
              .map((id) => (
                <option key={id} value={id}>
                  {id}
                </option>
              ))}
          </Select>
        </FormField>
      )}
      {splitting && (
        <div className="flex flex-col gap-2" role="group" aria-label="Traffic split">
          <div className="flex items-center justify-between font-mono text-xs">
            <span>
              {weights.primary} <strong>{weights.primaryWeight}%</strong>
            </span>
            <span>
              <strong>{100 - weights.primaryWeight}%</strong> {weights.secondary}
            </span>
          </div>
          <input
            type="range"
            aria-label={`Share of traffic for ${weights.primary}`}
            min={1}
            max={99}
            value={weights.primaryWeight}
            onChange={(event) => set({ primaryWeight: Number(event.target.value) })}
            className="w-full accent-accent"
          />
          <div className="flex gap-2">
            {SPLIT_PRESETS.map((share) => (
              <button
                key={share}
                type="button"
                onClick={() => set({ primaryWeight: share })}
                className={cn(
                  "rounded-control border px-2 py-0.5 font-mono text-2xs",
                  weights.primaryWeight === share
                    ? "border-accent bg-accent-muted text-accent"
                    : "border-border text-fg-muted hover:bg-bg-muted",
                )}
              >
                {share} / {100 - share}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
