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
  return (
    <Dialog open={revisionId !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent size="lg">
        <DialogHeader icon={<Layers />}>
          <DialogTitle>Revision {revisionId}</DialogTitle>
          <DialogDescription>
            Immutable. Its package is stored, never fetched, and nothing runs it.
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
            <DefinitionList columns={2}>
              <Definition label="ARN" value={revision.revisionArn} copyable full />
              <Definition label="State" value={<StateBadge state={revision.state} />} />
              <Definition label="Created" value={formatDate(revision.createdAt)} />
              <Definition label="Description" value={revision.description} variant="prose" full />
              <Definition label="S3 bucket" value={s3?.bucket} copyable />
              <Definition label="Object key" value={s3?.key} copyable />
              <Definition label="Object version" value={s3?.versionId} />
              <Definition label="Runtime" value={revision.buildConfig?.runtimeConfig?.runtime} />
              <Definition label="Execution role" value={service?.executionRoleArn} copyable full />
              <Definition label="Timeout" value={`${service?.timeoutSeconds ?? "—"} s`} />
              <Definition label="Max concurrency" value={service?.maxConcurrencyPerEnvironment} />
              <Definition label="KMS key" value={revision.kmsKeyArn} full />
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
