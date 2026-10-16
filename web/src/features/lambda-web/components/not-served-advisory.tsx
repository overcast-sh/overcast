import { Advisory } from "@/components/ui/advisory"

/**
 * Says plainly what the control plane stops short of: an endpoint's domain is
 * allocated and returned, but no server runs behind it, so opening it gets
 * nothing. Shown wherever a domain name is.
 */
export function NotServedAdvisory({ className }: { className?: string }) {
  return (
    <Advisory
      tone="info"
      title="Endpoint domains are not served yet"
      docsPath="services/lambda-web/limitations.md#stored-never-run"
      className={className}
    >
      Overcast allocates each endpoint&apos;s domain name and returns it, but nothing listens on it
      and a revision&apos;s code is never fetched or run. Treat a domain as a name, not a URL.
    </Advisory>
  )
}
