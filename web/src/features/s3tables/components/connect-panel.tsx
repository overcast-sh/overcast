import { Plug } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { CopyButton } from "@/components/ui/copy-button"
import { HighlightedCode } from "@/components/ui/highlighted-code"
import { Tab, TabList, TabPanel, Tabs } from "@/components/ui/tabs"
import { useEndpoint } from "@/hooks/use-endpoint"
import { useLocalStorage } from "@/hooks/use-local-storage"
import {
  connectSnippets,
  unsignedCatalogUri,
  type ConnectClient,
  type ConnectTarget,
} from "../connect-snippets"

/** The docs fence language, as a grammar the highlighter has — Java properties it has not. */
const HIGHLIGHT = { python: "python", bash: "bash", sql: "sql", properties: null } as const

/**
 * *Connect a client*: ready-to-paste configuration for PyIceberg, Spark,
 * Trino, DuckDB and the AWS CLI, with this emulator's endpoint and region and
 * the bucket's ARN as the warehouse. On a table's page each ends by reading
 * that table. The client picked last is remembered for the next page.
 */
export function ConnectPanel({
  warehouseArn,
  table,
  className,
}: Pick<ConnectTarget, "warehouseArn" | "table"> & { className?: string }) {
  const { baseUrl, region } = useEndpoint()
  const target: ConnectTarget = {
    endpoint: baseUrl || window.location.origin,
    region,
    warehouseArn,
    table,
  }
  const snippets = connectSnippets(target)
  const [client, setClient] = useLocalStorage<ConnectClient>("s3tables:connect-client", "pyiceberg")
  const selected = snippets.some((s) => s.client === client) ? client : snippets[0].client
  return (
    <Card className={className} aria-labelledby="s3tables-connect-title">
      <CardHeader className="flex-row items-center gap-2">
        <Plug aria-hidden className="size-3.5 text-fg-subtle" />
        <CardTitle id="s3tables-connect-title">Connect a client</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Tabs selectedKey={selected} onSelectionChange={(key) => setClient(key as ConnectClient)}>
          <TabList aria-label="Client" className="gap-4">
            {snippets.map((s) => (
              <Tab key={s.client} id={s.client} className="whitespace-nowrap">
                {s.label}
              </Tab>
            ))}
          </TabList>
          {snippets.map((s) => (
            <TabPanel key={s.client} id={s.client} className="relative pt-3">
              <HighlightedCode
                text={s.code}
                language={HIGHLIGHT[s.language]}
                className="max-h-80 overflow-auto rounded-md border border-border bg-bg p-3 pr-9 font-mono text-xs leading-5 whitespace-pre"
              />
              <CopyButton
                value={s.code}
                noun={`${s.label} snippet`}
                tone="inline"
                className="absolute top-5 right-2"
              />
            </TabPanel>
          ))}
        </Tabs>
        <p className="text-xs text-fg-muted">
          Any access key works: Overcast checks the SigV4 signing name{" "}
          <code className="font-mono">s3tables</code>, not the signature. A client that cannot sign
          uses the same catalog unsigned at
        </p>
        <span className="flex min-w-0 items-center gap-1.5 font-mono text-xs text-fg">
          <span className="truncate">{unsignedCatalogUri(target)}</span>
          <CopyButton
            value={unsignedCatalogUri(target)}
            noun="unsigned catalog URL"
            tone="inline"
          />
        </span>
      </CardContent>
    </Card>
  )
}
