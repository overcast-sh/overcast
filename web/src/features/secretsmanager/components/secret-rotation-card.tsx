/**
 * Rotation status and history for one secret.
 *
 * Three questions this answers, in the order someone debugging a rotation asks
 * them: is rotation on and against what, when did it last run and when will it
 * run next, and — when the last run failed — which of AWS's four steps it died
 * at. The step strip is the whole reason this card exists: a rotation that
 * fails at `testSecret` and one that fails at `finishSecret` mean completely
 * different things, and "rotation failed" alone tells you neither.
 */

import { Badge } from "@/components/ui/badge"
import { Definition, DefinitionCard } from "@/components/ui/definition-card"
import { CodeBlock, SectionLabel } from "@/components/ui/primitives"
import { ResourceTable } from "@/components/ui/resource-table"
import { formatDate, formatQuantity } from "@/lib/format"
import { cn } from "@/lib/utils"
import type { SecretRotationStatus } from "@/services/api/secretsmanager"

interface Props {
  status?: SecretRotationStatus
}

function stageVariant(stage: string) {
  if (stage === "AWSCURRENT") return "success" as const
  if (stage === "AWSPENDING") return "warning" as const
  return "default" as const
}

/**
 * How a step reads given where the last attempt got to: everything before the
 * recorded step ran, the recorded step is where it stopped, everything after
 * never started.
 */
function stepState(
  step: string,
  steps: string[],
  attemptStep?: string,
  attemptStatus?: string,
): { label: string; variant: "success" | "danger" | "default" } {
  if (attemptStatus === "Succeeded") return { label: "ran", variant: "success" }
  if (!attemptStep) return { label: "—", variant: "default" }
  const reached = steps.indexOf(attemptStep)
  const index = steps.indexOf(step)
  if (index < reached) return { label: "ran", variant: "success" }
  if (index === reached) return { label: "failed", variant: "danger" }
  return { label: "not reached", variant: "default" }
}

function scheduleText(rules: SecretRotationStatus["rotationRules"]): string {
  if (!rules) return "—"
  if (rules.AutomaticallyAfterDays) {
    const d = rules.AutomaticallyAfterDays
    return `Every ${formatQuantity(d, "day")}`
  }
  return rules.ScheduleExpression ?? "—"
}

export function SecretRotationCard({ status }: Props) {
  if (!status) return null

  const attempt = status.lastAttempt
  const failed = attempt?.status === "Failed"

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <SectionLabel>Rotation</SectionLabel>
        <Badge variant={status.rotationEnabled ? "success" : "default"}>
          {status.rotationEnabled ? "Enabled" : "Disabled"}
        </Badge>
      </div>

      <DefinitionCard>
        <Definition
          label="Rotation function"
          value={status.rotationLambdaArn || "Not configured"}
          full
        />
        <Definition label="Schedule" value={scheduleText(status.rotationRules)} />
        <Definition
          label="Last rotated"
          value={status.lastRotatedDate ? formatDate(status.lastRotatedDate * 1000) : "Never"}
        />
        <Definition
          label="Next rotation"
          value={
            status.nextRotationDate ? formatDate(status.nextRotationDate * 1000) : "Not scheduled"
          }
        />
      </DefinitionCard>

      {/* Version stages — where AWSCURRENT/AWSPENDING/AWSPREVIOUS actually sit.
          #1327 wave D: converted rather than left bespoke. It is a list of
          resources (secret versions) and the conversion is what gives it the
          "no versions" empty state it never had — a secret whose versions came
          back empty used to render a headers-only table — plus newest-first
          ordering that says so instead of trusting the API's. */}
      <div className="overflow-hidden rounded-md border border-border">
        <ResourceTable
          variant="embedded"
          query={{ data: status.versions, isLoading: false }}
          noun="versions"
          emptyTitle="No versions"
          rowKey={(v) => v.versionId}
          defaultSort={{ id: "created", desc: true }}
          columns={[
            {
              id: "version",
              header: "Version",
              cellClassName: "font-mono text-xs",
              sortValue: (v) => v.versionId,
              cell: (v) => v.versionId,
            },
            {
              id: "stages",
              header: "Staging labels",
              cell: (v) => (
                <div className="flex flex-wrap gap-1">
                  {v.stages.length === 0 ? (
                    <span className="text-fg-subtle">—</span>
                  ) : (
                    v.stages.map((s) => (
                      <Badge key={s} variant={stageVariant(s)}>
                        {s}
                      </Badge>
                    ))
                  )}
                </div>
              ),
            },
            {
              id: "created",
              header: "Created",
              sortValue: (v) => v.createdDate,
              cell: (v) => formatDate(v.createdDate * 1000),
            },
          ]}
        />
      </div>

      {/* Last attempt — including which of the four steps it stopped at. */}
      {attempt ? (
        <div className="flex flex-col gap-2 rounded-md border border-border p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm text-fg">Last attempt</span>
            <Badge variant={failed ? "danger" : "success"}>{attempt.status ?? "Unknown"}</Badge>
            {attempt.trigger ? <Badge variant="outline">{attempt.trigger}</Badge> : null}
            <span className="text-xs text-fg-muted">
              {attempt.completedDate ? formatDate(attempt.completedDate * 1000) : ""}
            </span>
          </div>

          <div className="flex flex-wrap gap-2">
            {status.steps.map((step) => {
              const state = stepState(step, status.steps, attempt.step, attempt.status)
              return (
                <div
                  key={step}
                  className={cn(
                    "flex items-center gap-1.5 rounded-control border px-2 py-1",
                    state.variant === "danger" ? "border-danger" : "border-border",
                  )}
                >
                  <span className="font-mono text-xs text-fg">{step}</span>
                  <Badge variant={state.variant}>{state.label}</Badge>
                </div>
              )
            })}
          </div>

          {attempt.error ? (
            <CodeBlock className="max-h-40 overflow-y-auto text-xs">{attempt.error}</CodeBlock>
          ) : null}
        </div>
      ) : (
        <p className="text-sm text-fg-muted">This secret has not been rotated yet.</p>
      )}
    </section>
  )
}
