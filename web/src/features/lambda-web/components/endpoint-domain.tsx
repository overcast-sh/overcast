import { CopyButton } from "@/components/ui/copy-button"

/**
 * An endpoint's domain name as text with a copy button — never a link, since
 * nothing serves it.
 */
export function EndpointDomain({ domainName }: { domainName: string | undefined }) {
  if (!domainName) return <span className="text-fg-subtle">—</span>
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <span className="truncate font-mono text-xs" title={domainName}>
        {domainName}
      </span>
      <CopyButton value={domainName} noun="domain name" tone="inline" />
    </span>
  )
}
