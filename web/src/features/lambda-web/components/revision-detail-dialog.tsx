import { useQuery } from "@tanstack/react-query"
import { Layers } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Definition, DefinitionList } from "@/components/ui/definition-card"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { SkeletonRows } from "@/components/ui/skeleton"
import { formatDate } from "@/lib/format"
import { webRevisionQueryOptions } from "../data"
import { defaultLogGroup } from "../models"
import { FailureNotice } from "./failure-notice"
import { LogLinks } from "./log-links"
import { StateBadge } from "./state-badge"

interface RevisionDetailDialogProps {
  functionName: string
  revisionId: string | undefined
  onClose: () => void
}

/** One revision's code and service configuration, read-only: a revision never changes. */
export function RevisionDetailDialog({
  functionName,
  revisionId,
  onClose,
}: RevisionDetailDialogProps) {
  const { data: revision, error } = useQuery(
    webRevisionQueryOptions(functionName, revisionId ?? ""),
  )
  const s3 = revision?.buildConfig?.codeConfig?.s3Object
  const service = revision?.serviceConfig
  const env = Object.entries(service?.environmentVariables ?? {})
  const logging = service?.telemetryConfig?.loggingConfig
  const logGroup = logging?.logGroup || defaultLogGroup(functionName)
  return (
    <Dialog open={revisionId !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent size="lg">
        <DialogHeader icon={<Layers />}>
          <DialogTitle>Revision {revisionId}</DialogTitle>
          <DialogDescription>
            Immutable. Its package runs in a container, started by the first request.
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          {error ? (
            <p role="alert" className="text-xs text-danger">
              {error.message}
            </p>
          ) : !revision ? (
            <SkeletonRows rows={6} noun="revision" />
          ) : (
            <div className="flex flex-col gap-4">
              {revision.state === "Failed" && (
                <FailureNotice title="This revision failed and cannot be used">
                  {revision.stateReason && <p>{revision.stateReason}</p>}
                  {revision.errors && revision.errors.length > 0 && (
                    <ul className="mt-1 flex list-disc flex-col gap-0.5 pl-4">
                      {revision.errors.map((e, i) => (
                        <li key={i}>
                          <span className="font-mono">{e.errorCode}</span>
                          {e.attribute && (
                            <>
                              {" "}
                              on <span className="font-mono">{e.attribute}</span>
                            </>
                          )}
                          {e.errorMessage && <>: {e.errorMessage}</>}
                        </li>
                      ))}
                    </ul>
                  )}
                  <p className="mt-1">Revisions are immutable: create a new one to fix it.</p>
                </FailureNotice>
              )}
              {revision.state === "Pending" && (
                <p className="text-xs text-fg-muted">
                  Overcast is preparing this revision&apos;s package. This page updates when it is
                  ready.
                </p>
              )}
              <DefinitionList columns={2}>
                <Definition label="ARN" value={revision.revisionArn} copyable full />
                <Definition label="State" value={<StateBadge state={revision.state} />} />
                <Definition label="State reason" value={revision.stateReason} variant="prose" />
                <Definition label="Created" value={formatDate(revision.createdAt)} />
                <Definition label="Description" value={revision.description} variant="prose" full />
                <Definition label="S3 bucket" value={s3?.bucket} copyable />
                <Definition label="Object key" value={s3?.key} copyable />
                <Definition label="Object version" value={s3?.versionId} />
                <Definition label="Runtime" value={revision.buildConfig?.runtimeConfig?.runtime} />
                <Definition
                  label="Execution role"
                  value={service?.executionRoleArn}
                  copyable
                  full
                />
                <Definition label="Timeout" value={`${service?.timeoutSeconds ?? "—"} s`} />
                <Definition label="Max concurrency" value={service?.maxConcurrencyPerEnvironment} />
                <Definition label="KMS key" value={revision.kmsKeyArn} full />
                <Definition label="Logs" value={<LogLinks logGroup={logGroup} />} full />
                <Definition label="Application log level" value={logging?.applicationLogLevel} />
                <Definition label="System log level" value={logging?.systemLogLevel} />
                <Definition
                  label="Environment variables"
                  full
                  value={
                    env.length === 0
                      ? undefined
                      : env.map(([key, value]) => (
                          <span key={key} className="block break-all">
                            {key}={value}
                          </span>
                        ))
                  }
                />
              </DefinitionList>
            </div>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
