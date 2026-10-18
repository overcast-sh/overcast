import { ExternalLink, Globe } from "lucide-react"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
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
import { useEndpoint } from "@/hooks/use-endpoint"
import type { WebEndpoint } from "@/services/api/lambda-web"
import { curlSigV4Example, endpointAddress } from "../endpoint-access"
import { describeServes } from "../models"
import { FailureNotice } from "./failure-notice"
import { StateBadge } from "./state-badge"
import { TestRequestPanel } from "./test-request-panel"
import { TrafficPanel } from "./traffic"

interface EndpointDetailDialogProps {
  functionName: string
  /** The endpoint to show; the dialog is closed without one. */
  endpoint: WebEndpoint | undefined
  /** The function's Region. */
  region: string
  /** The endpoint's environments right now, when the topology has said. */
  environments: number | undefined
  onClose: () => void
}

/**
 * One endpoint: its state, how to reach it, what it serves and how it is
 * scaling, its last hour of traffic, and a form to send it a request.
 */
export function EndpointDetailDialog({
  functionName,
  endpoint,
  region,
  environments,
  onClose,
}: EndpointDetailDialogProps) {
  return (
    <Dialog open={endpoint !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent size="lg" className="max-w-3xl">
        <DialogHeader icon={<Globe />}>
          <DialogTitle>Endpoint {endpoint?.endpointName}</DialogTitle>
          <DialogDescription>
            Serves HTTP from containers that start on the first request and stop when idle.
          </DialogDescription>
        </DialogHeader>
        <DialogBody>
          {endpoint && (
            <EndpointDetail
              functionName={functionName}
              endpoint={endpoint}
              region={region}
              environments={environments}
            />
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

function EndpointDetail({
  functionName,
  endpoint,
  region,
  environments,
}: Omit<EndpointDetailDialogProps, "onClose"> & { endpoint: WebEndpoint }) {
  const { baseUrl } = useEndpoint()
  const domainName = endpoint.domainName ?? ""
  const address = domainName ? endpointAddress(domainName, baseUrl, region) : undefined
  const iam = endpoint.authType === "IamAuth"
  return (
    <div className="flex flex-col gap-5">
      {endpoint.state === "Failed" && (
        <FailureNotice title="This endpoint failed">{endpoint.stateReason}</FailureNotice>
      )}
      {endpoint.updateStatus === "Failed" && (
        <FailureNotice title="The last update failed">{endpoint.updateStatusReason}</FailureNotice>
      )}
      <DefinitionList columns={2}>
        <Definition label="State" value={<StateBadge state={endpoint.state} />} />
        <Definition
          label="Last update"
          value={
            endpoint.updateStatus ? (
              <span className="inline-flex flex-wrap items-center gap-1.5">
                <StateBadge state={endpoint.updateStatus} />
                {endpoint.updateStatus !== "Failed" && endpoint.updateStatusReason}
              </span>
            ) : undefined
          }
        />
        <Definition label="Serves" value={describeServes(endpoint.revisionWeights)} />
        <Definition
          label="Environments"
          value={
            environments === undefined
              ? undefined
              : environments === 0
                ? "0 · scaled to zero"
                : `${environments} running or starting`
          }
        />
        <Definition label="Domain name" value={domainName} copyable full />
        {address && (
          <>
            {address.hostUrl && (
              <Definition
                label="Endpoint URL"
                full
                value={<UrlValue url={address.hostUrl} openable={!iam} noun="endpoint URL" />}
              />
            )}
            <Definition
              label="Invoke URL"
              full
              value={<UrlValue url={address.invokeUrl} openable={!iam} noun="invoke URL" />}
            />
          </>
        )}
      </DefinitionList>
      {address && (
        <p className="-mt-3 text-xs text-fg-muted">
          {address.hostUrl
            ? "The endpoint URL needs this console's host to resolve subdomains. "
            : "This console reaches the emulator on a host that does not take subdomains, so the endpoint's own domain is not openable from here. "}
          The invoke URL works with any Host: the server forwards the endpoint&apos;s domain to the
          application itself.
        </p>
      )}
      {iam && address && (
        <section aria-label="Calling an IAM auth endpoint" className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">Calling it</h3>
          <p className="text-xs text-fg-muted">
            This endpoint uses IAM auth: requests must be signed with SigV4 (service{" "}
            <span className="font-mono">lambda</span>), so a browser cannot open it.
          </p>
          <pre className="overflow-x-auto rounded-md bg-bg-muted p-2 font-mono text-xs">
            {curlSigV4Example(address.hostUrl ?? address.invokeUrl, region)}
          </pre>
          <CopyButton
            value={curlSigV4Example(address.hostUrl ?? address.invokeUrl, region)}
            noun="curl command"
            className="self-start"
          />
        </section>
      )}
      <TrafficPanel functionName={functionName} endpointName={endpoint.endpointName ?? ""} />
      {domainName && (
        <TestRequestPanel
          domainName={domainName}
          region={region}
          authType={endpoint.authType}
          environments={environments}
        />
      )}
    </div>
  )
}

function UrlValue({ url, openable, noun }: { url: string; openable: boolean; noun: string }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      {openable ? (
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex min-w-0 items-center gap-1 text-accent hover:underline"
        >
          <span className="break-all">{url}</span>
          <ExternalLink aria-hidden className="size-3 shrink-0" />
        </a>
      ) : (
        <span className="break-all">{url}</span>
      )}
      <CopyButton value={url} noun={noun} tone="inline" />
    </span>
  )
}
