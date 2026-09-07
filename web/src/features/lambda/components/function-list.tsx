import { useState } from "react"
import { useNavigate } from "@tanstack/react-router"
import { useQuery } from "@tanstack/react-query"
import { Eye, Zap } from "lucide-react"
import {
  lambdaFunctionsQueryOptions,
  lambdaKeys,
  deleteFunctionMutationOptions,
} from "@/features/lambda/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import type { LambdaFunction } from "@/types"
import { RegionElsewhereNotice } from "@/features/preflight/components/region-elsewhere-notice"
import {
  CreateAction,
  RefreshAction,
  ResourceListPage,
  ResourceName,
  RowAction,
} from "@/components/ui/resource-list-page"
import { ResourceTable, type ResourceTableSort } from "@/components/ui/resource-table"
import { CreateFunctionWizard } from "./create-wizard"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { useDebugTargets } from "@/features/debugger/hooks"
import { indexTargetsByResource } from "@/features/debugger/data"
import { DebugStateBadge } from "@/features/debugger/components/debug-state-badge"
import { cn } from "@/lib/utils"

// ─── Component ───────────────────────────────────────────────────────────────

interface FunctionListProps {
  /** Current table sort — owned by the route's `sort` search param, see `useSortSearchParam`. */
  sort?: ResourceTableSort
  onSortChange?: (next: ResourceTableSort | undefined) => void
}

export function FunctionList({ sort, onSortChange }: FunctionListProps = {}) {
  const navigate = useNavigate()
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<LambdaFunction>()
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

  const {
    data: functions = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(lambdaFunctionsQueryOptions())

  // One list query for every row's debug badge, polled while this page is
  // mounted. A server without the endpoint errors, which reads as no targets.
  const { data: debugTargets } = useDebugTargets()
  const debugIndex = indexTargetsByResource(debugTargets, "lambda")

  const deleteMut = useResourceMutation({
    options: deleteFunctionMutationOptions(),
    invalidateKeys: [lambdaKeys.functions()],
    successTitle: "Function deleted",
    successDescription: ({ name }) => name,
    successVariant: "default",
    errorTitle: "Delete failed",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const activeCount = functions.filter((fn) => fn.State === "Active").length

  return (
    <ResourceListPage
      title="Lambda Functions"
      count={functions.length}
      meta={functions.length > 0 ? `${activeCount} active` : undefined}
      actions={
        <>
          <ServiceDocsButton
            service="lambda"
            label="Lambda"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create function</CreateAction>
        </>
      }
    >
      <ResourceTable
        query={{ data: functions, isLoading, error }}
        noun="functions"
        emptyIcon={Zap}
        emptyTitle="No functions yet"
        emptyDescription="Create a function to get started."
        emptyAction={
          <CreateAction onClick={() => setShowCreate(true)}>Create function</CreateAction>
        }
        emptyExtra={<RegionElsewhereNotice kind="lambda-functions" noun="functions" />}
        errorTitle="Failed to load functions"
        sort={sort}
        onSortChange={onSortChange}
        rowKey={(fn) => fn.FunctionArn ?? fn.FunctionName ?? ""}
        onRowClick={(fn) =>
          navigate({ to: "/lambda/$name", params: { name: fn.FunctionName ?? "" } })
        }
        columns={[
          {
            // Explicit ids on the sortable columns: the id is the `?sort=` token,
            // and a reworded header must not break a link somebody saved.
            id: "name",
            header: "Name",
            sortValue: (fn) => fn.FunctionName,
            cell: (fn) => <ResourceName icon={Zap} name={fn.FunctionName ?? ""} />,
          },
          { header: "Runtime", cellClassName: "text-fg-muted", cell: (fn) => fn.Runtime },
          { header: "Handler", cellClassName: "text-fg-muted", cell: (fn) => fn.Handler },
          {
            header: "Memory",
            cellClassName: "text-fg-muted",
            sortValue: (fn) => fn.MemorySize ?? 128,
            cell: (fn) => `${fn.MemorySize ?? 128} MB`,
          },
          {
            header: "Timeout",
            cellClassName: "text-fg-muted",
            sortValue: (fn) => fn.Timeout ?? 3,
            cell: (fn) => `${fn.Timeout ?? 3}s`,
          },
          {
            header: "State",
            cell: (fn) => {
              const debug = debugIndex.get(fn.FunctionName ?? "")
              return (
                <span className="inline-flex items-center gap-2">
                  <span
                    className={cn(
                      "font-mono text-xs",
                      fn.State === "Active" ? "font-medium text-success" : "text-fg-muted",
                    )}
                  >
                    {fn.State}
                  </span>
                  {debug && <DebugStateBadge state={debug.state} />}
                </span>
              )
            },
          },
        ]}
        rowActions={(fn) => (
          <RowAction
            label={`View ${fn.FunctionName ?? ""}`}
            onClick={() =>
              navigate({ to: "/lambda/$name", params: { name: fn.FunctionName ?? "" } })
            }
          >
            <Eye className="h-3.5 w-3.5" />
          </RowAction>
        )}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (fn) => ({ name: fn.FunctionName ?? "" }),
          label: (fn) => fn.FunctionName ?? "",
          noun: "function",
          title: "Delete function",
          description: (fn) => (
            <>
              Delete <span className="font-mono font-medium">{fn.FunctionName}</span>, including
              every published version and alias? This cannot be undone. To remove a single version,
              use the function's Versions tab.
            </>
          ),
        }}
      />

      <CreateFunctionWizard open={showCreate} onOpenChange={setShowCreate} />
    </ResourceListPage>
  )
}
