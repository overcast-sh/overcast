import { ExternalLink, Route } from "lucide-react"
import { CopyButton } from "@/components/ui/copy-button"
import { useEndpoint } from "@/hooks/use-endpoint"
import { endpointAddress } from "../endpoint-access"

interface EndpointDomainProps {
  domainName: string | undefined
  /** `IamAuth` endpoints cannot be opened in a browser: they want a SigV4 signature. */
  authType: string | undefined
  /** The Region to fall back to when the domain name carries none. */
  region: string
}

/**
 * Where an endpoint is reached, as the table shows it: its URL on its own
 * domain when this console reaches the emulator on a host that takes
 * subdomains, else the invoke URL that works with any Host — a link to open
 * it, and a copy button for each. An `IamAuth` endpoint is shown as text, not a
 * link: a browser cannot sign.
 */
export function EndpointDomain({ domainName, authType, region }: EndpointDomainProps) {
  const { baseUrl } = useEndpoint()
  if (!domainName) return <span className="text-fg-subtle">—</span>
  const { hostUrl, invokeUrl } = endpointAddress(domainName, baseUrl, region)
  const url = hostUrl ?? invokeUrl
  const iam = authType === "IamAuth"
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      {iam ? (
        <span
          className="max-w-72 truncate font-mono text-xs"
          title="IAM auth: calls must be signed with SigV4, so a browser cannot open this. Open the endpoint for a curl example."
        >
          {url}
        </span>
      ) : (
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          title={`Open ${url}`}
          className="inline-flex max-w-72 items-center gap-1 font-mono text-xs text-accent hover:underline"
        >
          <span className="truncate">{url}</span>
          <ExternalLink aria-hidden className="size-3 shrink-0" />
        </a>
      )}
      <CopyButton value={url} noun={hostUrl ? "endpoint URL" : "invoke URL"} tone="inline" />
      {hostUrl && (
        <CopyButton
          value={invokeUrl}
          noun="invoke URL"
          tone="inline"
          icon={<Route className="size-3.5" />}
        />
      )}
    </span>
  )
}
