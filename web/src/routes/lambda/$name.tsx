/**
 * Lambda function detail page — Overview, Code, Test, Debug, and Configuration tabs.
 */
import { useState, useCallback, useEffect, useMemo, useRef } from "react"
import { createFileRoute, useNavigate } from "@tanstack/react-router"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Button } from "@/components/ui/button"
import { Spinner, PageHeader } from "@/components/ui/primitives"
import { useToast } from "@/components/ui/toast"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import {
  lambdaFunctionsQueryOptions,
  lambdaSourceQueryOptions,
  putSourceMutationOptions,
  lambdaKeys,
} from "@/features/lambda/data"
import { FunctionOverview } from "@/features/lambda/components/function-overview"
import { CodeTab } from "@/features/lambda/components/code-tab"
import { TestTab } from "@/features/lambda/components/test-tab"
import { useLambdaInvoke } from "@/features/lambda/use-invoke"
import { VersionsTab } from "@/features/lambda/components/versions-tab"
import { MonitorTab } from "@/features/lambda/components/monitor-tab"
import { ConfigurationTab } from "@/features/lambda/components/configuration-tab"
import { TriggersTab } from "@/features/lambda/components/triggers-tab"
import { DebugTargetPanel } from "@/features/debugger/components/debug-panel"
import { DebugSessionControls } from "@/features/debugger/components/debug-session-controls"
import { OnPause } from "@/features/debugger/components/on-pause"
import { DebugSessionProvider } from "@/features/debugger/session/provider"
import { lambda } from "@/services/api"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"

export const Route = createFileRoute("/lambda/$name")({
  head: ({ params }) => ({ meta: [{ title: `${params.name} — Lambda — Overcast` }] }),
  component: FunctionDetail,
})

type TabKey = "code" | "test" | "debug" | "versions" | "monitor" | "configuration" | "triggers"

const VALID_TABS = new Set<TabKey>([
  "code",
  "test",
  "debug",
  "versions",
  "monitor",
  "configuration",
  "triggers",
])

function FunctionDetail() {
  const { name } = Route.useParams()
  const navigate = useNavigate()

  const hashToTab = useCallback((): TabKey => {
    const h = window.location.hash.slice(1) as TabKey
    return VALID_TABS.has(h) ? h : "code"
  }, [])

  const [activeTab, setActiveTab] = useState<TabKey>(hashToTab)

  // Sync state when the user navigates back/forward
  useEffect(() => {
    const onHashChange = () => setActiveTab(hashToTab())
    window.addEventListener("hashchange", onHashChange)
    return () => window.removeEventListener("hashchange", onHashChange)
  }, [hashToTab])

  const switchTab = useCallback((t: string) => {
    window.location.hash = t
    setActiveTab(t as TabKey)
  }, [])

  const { data: functions, isLoading: functionsLoading } = useQuery(lambdaFunctionsQueryOptions())
  const fn = functions?.find((f) => f.FunctionName === name)

  const {
    data: source,
    isLoading: sourceLoading,
    isError: sourceError,
  } = useQuery(lambdaSourceQueryOptions(name))

  const [editedFiles, setEditedFiles] = useState<Record<string, string>>({})
  const [activeFilePath, setActiveFilePath] = useState<string | undefined>(undefined)
  const initialSource = source?.source ?? ""
  const activeFile = activeFilePath ?? source?.filename ?? ""
  const currentEditorValue = editedFiles[activeFile] ?? initialSource

  const { mutate: deploy, isPending: isDeploying } = useResourceMutation({
    options: putSourceMutationOptions(),
    invalidateKeys: [lambdaKeys.sourceFiles(name), lambdaKeys.functions()],
    successTitle: "Deployed",
    successDescription: () => `${name} updated successfully.`,
    errorTitle: "Deploy failed",
    onSuccess: (updated) => {
      setEditedFiles((prev) => ({ ...prev, [updated.filename]: updated.source }))
    },
  })

  const handleDeploy = useCallback(() => {
    if (!source) return
    deploy({ name, source: currentEditorValue, filename: activeFile })
  }, [name, source, currentEditorValue, activeFile, deploy])

  // The console debug session (docs/plans/compute-debugger-console.md § 3.4)
  // reads deployed files through the same source endpoint as the Code tab.
  const fetchDeployedFile = useCallback(
    (path: string) => lambda.getSource(name, path).then((data) => data.source),
    [name],
  )
  const deployedFiles = useMemo(() => source?.files?.map((f) => f.name), [source?.files])
  // A pause anywhere lands the reader on the code, at the paused line. The
  // invoke lives here rather than in the Test tab, which that switch
  // unmounts, so the result still lands on the Test tab after Continue.
  const showCodeOnPause = useCallback(() => switchTab("code"), [switchTab])
  const invoke = useLambdaInvoke(name)

  // The result lands on the Test tab. When it arrives while another tab is
  // up — the pause moved the reader to the code, and Continue ran the
  // function to its end — a toast says so, or the reader is left looking at
  // a pane that went quiet with no idea the invocation finished.
  const { toast } = useToast()
  const activeTabRef = useRef(activeTab)
  useEffect(() => {
    activeTabRef.current = activeTab
  }, [activeTab])
  const { isPending: invokePending, result: invokeResult, error: invokeError } = invoke
  useEffect(() => {
    if (invokePending || (!invokeResult && !invokeError) || activeTabRef.current === "test") return
    const failed = invokeError !== null || Boolean(invokeResult?.functionError)
    toast({
      variant: failed ? "danger" : "success",
      title: invokeError ? "Invocation failed" : failed ? "Execution failed" : "Execution succeeded",
      description: "The result is on the Test tab.",
      descriptionKind: "prose",
    })
  }, [invokePending, invokeResult, invokeError, toast])

  // Hot reload replaced the container under a debug session: the files the
  // Code tab shows may have changed with it.
  const queryClient = useQueryClient()
  const refreshSource = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: lambdaKeys.sourceFiles(name) })
  }, [queryClient, name])

  if (functionsLoading) {
    return (
      <div className="flex items-center justify-center py-32">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!fn) {
    return (
      <div className="flex flex-col items-center gap-3 py-32 text-center">
        <p className="text-fg-muted">Function not found.</p>
        <Button variant="secondary" size="sm" onClick={() => navigate({ to: "/lambda" })}>
          Back to functions
        </Button>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4 p-4 pb-8">
      <PageHeader
        title={fn.FunctionName ?? ""}
        description={fn.Description || undefined}
        actions={
          activeTab === "code" ? (
            <Button onClick={handleDeploy} disabled={isDeploying || sourceLoading} size="md">
              {isDeploying ? <Spinner className="mr-2 h-3.5 w-3.5" /> : null}
              Deploy
            </Button>
          ) : null
        }
      />

      <ApplicationOwnershipBanner candidates={[fn.FunctionArn, fn.FunctionName]} />

      {/* ── Function overview ──────────────────────────────────────────── */}
      <FunctionOverview fn={fn} />

      {/* ── Tabs ───────────────────────────────────────────────────────── */}
      <DebugSessionProvider
        service="lambda"
        resource={name}
        fetchFile={fetchDeployedFile}
        files={deployedFiles}
        onContainerReplaced={refreshSource}
      >
        <OnPause onPause={showCodeOnPause} />
        <Tabs selectedKey={activeTab} onSelectionChange={switchTab}>
          <TabList>
            <Tab id="code">Code</Tab>
            <Tab id="test">Test</Tab>
            <Tab id="debug">Debug</Tab>
            <Tab id="versions">Versions</Tab>
            <Tab id="monitor">Monitor</Tab>
            <Tab id="configuration">Configuration</Tab>
            <Tab id="triggers">Triggers</Tab>
          </TabList>

          {/*
          Only the code panel sits flush: its editor is a full-width bordered
          slab whose top edge continues the tab rule. Every other panel opens
          with bare text or a narrower card, which needs the rule to breathe.
        */}
          <TabPanel id="code">
            <CodeTab
              source={source}
              sourceLoading={sourceLoading}
              sourceError={sourceError}
              currentEditorValue={currentEditorValue}
              setEditedFiles={setEditedFiles}
              setActiveFilePath={setActiveFilePath}
              name={name}
              logGroup={fn.LoggingConfig?.LogGroup || `/aws/lambda/${name}`}
            />
          </TabPanel>
          <TabPanel id="test" className="pt-4">
            <TestTab name={name} timeoutSeconds={fn.Timeout ?? 3} invoke={invoke} />
          </TabPanel>
          <TabPanel id="debug" className="flex flex-col gap-4 pt-4">
            <DebugSessionControls service="lambda" resource={name} />
            <DebugTargetPanel service="lambda" resource={name} />
          </TabPanel>
          <TabPanel id="versions" className="pt-4">
            <VersionsTab name={name} />
          </TabPanel>
          <TabPanel id="monitor" className="pt-4">
            <MonitorTab fn={fn} />
          </TabPanel>
          <TabPanel id="configuration" className="pt-4">
            <ConfigurationTab fn={fn} />
          </TabPanel>
          <TabPanel id="triggers" className="pt-4">
            <TriggersTab name={name} />
          </TabPanel>
        </Tabs>
      </DebugSessionProvider>
    </div>
  )
}
