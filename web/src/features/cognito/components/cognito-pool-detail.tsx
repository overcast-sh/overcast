import { useState, useMemo, useRef, useEffect } from "react"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import {
  UserCheck,
  Plus,
  Trash2,
  RefreshCw,
  Search,
  Eye,
  EyeOff,
  Users,
  ShieldCheck,
  Settings,
  ChevronDown,
  ChevronRight,
  Pencil,
  Timer,
  Globe,
  KeyRound,
  X,
  Check,
  Paintbrush,
  UserCog,
  Mail,
  MessageSquare,
} from "lucide-react"
import {
  cognitoPoolDetailQueryOptions,
  cognitoUsersQueryOptions,
  cognitoGroupsQueryOptions,
  cognitoGroupMembersQueryOptions,
  cognitoClientsQueryOptions,
  cognitoClientDetailQueryOptions,
  cognitoUserDetailQueryOptions,
  cognitoKeys,
  cognitoBrandingQueryOptions,
  setBrandingMutationOptions,
  createUserMutationOptions,
  deleteUserMutationOptions,
  disableUserMutationOptions,
  enableUserMutationOptions,
  setUserPasswordMutationOptions,
  createGroupMutationOptions,
  deleteGroupMutationOptions,
  removeUserFromGroupMutationOptions,
  createClientMutationOptions,
  deleteClientMutationOptions,
  updateClientMutationOptions,
  updateUserAttributesMutationOptions,
  deleteUserAttributesMutationOptions,
  createDomainMutationOptions,
  deleteDomainMutationOptions,
  updatePoolPasswordPolicyMutationOptions,
  updatePoolSelfRegistrationMutationOptions,
  updatePoolEmailConfigMutationOptions,
  updatePoolVerificationMessagesMutationOptions,
} from "@/features/cognito/data"
import type {
  CognitoUser,
  PasswordPolicy,
  ManagedLoginBranding,
  AdminCreateUserConfig,
  EmailConfig,
  VerificationMessageTemplate,
} from "@/services/api/cognito"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Button } from "@/components/ui/button"
import { CopyButton } from "@/components/ui/copy-button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { Definition, DefinitionCard, DefinitionList } from "@/components/ui/definition-card"
// eslint-disable-next-line local/prefer-resource-table -- an attribute grid, not a list of resources
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ResourceTable } from "@/components/ui/resource-table"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { PageHeader, Spinner, EmptyState } from "@/components/ui/primitives"
import { ApplicationOwnershipBanner } from "@/components/application-ownership-banner"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { useToast } from "@/components/ui/toast"
import { formatDate, formatQuantity } from "@/lib/format"
import { fieldLabel, sectionLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { ArnText } from "@/components/ui/arn-link"
import type { CognitoClient } from "@/services/api/cognito"
import { endpointStore } from "@/services/endpoint-store"

interface Props {
  poolId: string
}

// ─── Pool Detail (root) ────────────────────────────────────────────────────

export function CognitoPoolDetail({ poolId }: Props) {
  const [tab, setTab] = useState("overview")

  const {
    data: pool,
    isLoading: poolLoading,
    isFetching: poolFetching,
    refetch: refetchPool,
  } = useQuery(cognitoPoolDetailQueryOptions(poolId))

  if (poolLoading) {
    return (
      <div className="flex w-full justify-center py-24">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!pool) {
    return (
      <div className="flex w-full flex-col gap-4">
        <EmptyState
          icon={<UserCheck className="h-8 w-8 opacity-40" />}
          title="User pool not found"
          description={`No pool with ID "${poolId}" exists.`}
        />
      </div>
    )
  }

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title={pool.name}
        actions={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void refetchPool()}
            disabled={poolFetching}
            title="Refresh"
          >
            <RefreshCw className={cn("h-4 w-4", poolFetching && "animate-spin")} />
          </Button>
        }
      />

      <ApplicationOwnershipBanner candidates={[pool.arn, pool.id, pool.name]} />

      <Tabs selectedKey={tab} onSelectionChange={setTab}>
        <TabList>
          <Tab id="overview">Overview</Tab>
          <Tab id="users">Users</Tab>
          <Tab id="groups">Groups</Tab>
          <Tab id="clients">App clients</Tab>
        </TabList>
        <TabPanel id="overview" className="pt-4">
          <OverviewTab pool={pool} poolId={poolId} />
        </TabPanel>
        <TabPanel id="users" className="pt-4">
          <UsersTab poolId={poolId} usernameAttributes={pool.usernameAttributes} />
        </TabPanel>
        <TabPanel id="groups" className="pt-4">
          <GroupsTab poolId={poolId} />
        </TabPanel>
        <TabPanel id="clients" className="pt-4">
          <ClientsTab poolId={poolId} />
        </TabPanel>
      </Tabs>
    </div>
  )
}

// ─── Overview tab ─────────────────────────────────────────────────────────

interface PoolSummary {
  id: string
  name: string
  arn: string
  creationDate?: string
  lastModifiedDate?: string
  estimatedNumberOfUsers: number
  domain?: string
  usernameAttributes?: string[]
  policies?: {
    passwordPolicy?: PasswordPolicy
  }
  adminCreateUserConfig?: AdminCreateUserConfig
  emailConfiguration?: EmailConfig
  verificationMessageTemplate?: VerificationMessageTemplate
}

function OverviewTab({ pool, poolId }: { pool: PoolSummary; poolId: string }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [domainInput, setDomainInput] = useState("")
  const [showDomainInput, setShowDomainInput] = useState(false)
  const [showLoginTest, setShowLoginTest] = useState(false)

  const createDomainMut = useResourceMutation({
    options: createDomainMutationOptions(),
    invalidateKeys: [cognitoKeys.pool(poolId)],
    successTitle: "Domain configured",
    onSuccess: () => {
      setShowDomainInput(false)
      setDomainInput("")
    },
  })

  const deleteDomainMut = useMutation({
    ...deleteDomainMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.pool(poolId) })
      toast({ title: "Domain removed" })
    },
  })

  return (
    <div className="flex flex-col gap-4">
      <DefinitionCard contentClassName="p-6">
        <Definition label="Pool ID" value={pool.id} copyable />
        <Definition label="Pool name" value={pool.name} />
        <Definition label="Created" value={formatDate(pool.creationDate)} />
        <Definition label="Last modified" value={formatDate(pool.lastModifiedDate)} />
        <Definition label="Estimated users" value={pool.estimatedNumberOfUsers} />
        <Definition label="ARN" value={<ArnText arn={pool.arn} />} copyable={pool.arn} full />
      </DefinitionCard>

      {/* ─── Managed Login / Domain ─────────────────────────────────── */}
      <Card>
        <CardContent className="flex flex-col gap-4 p-6">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Globe className="h-4 w-4 text-fg-muted" />
              <h3 className="font-mono text-sm font-medium">Managed Login</h3>
            </div>
            {pool.domain && !showLoginTest && (
              <Button size="sm" onClick={() => setShowLoginTest(true)}>
                <Paintbrush className="mr-1.5 h-3.5 w-3.5" /> Customize & Preview
              </Button>
            )}
          </div>

          {pool.domain ? (
            <div className="flex flex-col gap-3">
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm text-fg-muted">Domain:</span>
                <span className="font-mono text-sm">{pool.domain}</span>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 text-danger hover:text-danger"
                  title="Remove domain"
                  disabled={deleteDomainMut.isPending}
                  onClick={() => deleteDomainMut.mutate({ poolId, domain: pool.domain! })}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
              </div>
              {showLoginTest && (
                <ManagedLoginTestPanel poolId={poolId} onClose={() => setShowLoginTest(false)} />
              )}
            </div>
          ) : showDomainInput ? (
            <form
              className="flex items-center gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (domainInput.trim()) {
                  createDomainMut.mutate({ poolId, domain: domainInput.trim() })
                }
              }}
            >
              <Input
                placeholder="my-app-domain"
                value={domainInput}
                onChange={(e) => setDomainInput(e.target.value)}
                className="max-w-xs"
                autoFocus
              />
              <Button
                type="submit"
                size="sm"
                disabled={!domainInput.trim() || createDomainMut.isPending}
              >
                {createDomainMut.isPending ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
                Save
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => {
                  setShowDomainInput(false)
                  setDomainInput("")
                }}
              >
                Cancel
              </Button>
            </form>
          ) : (
            <div className="flex flex-col gap-1">
              <p className="text-sm text-fg-muted">
                Configure a domain to enable the hosted login UI (OAuth2 / OIDC).
              </p>
              <div>
                <Button size="sm" onClick={() => setShowDomainInput(true)}>
                  <Plus className="mr-1.5 h-3.5 w-3.5" /> Configure domain
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <PasswordPolicyCard pool={pool} poolId={poolId} />
      <SelfRegistrationCard pool={pool} poolId={poolId} />
      <EmailConfigCard pool={pool} poolId={poolId} />
      <VerificationMessagesCard pool={pool} poolId={poolId} />
    </div>
  )
}

// ─── Password Policy card ────────────────────────────────────────────────────

const DEFAULT_POLICY: PasswordPolicy = {
  minimumLength: 8,
  requireUppercase: false,
  requireLowercase: false,
  requireNumbers: false,
  requireSymbols: false,
  temporaryPasswordValidityDays: 7,
}

function PasswordPolicyCard({ pool, poolId }: { pool: PoolSummary; poolId: string }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<PasswordPolicy>(DEFAULT_POLICY)

  const current: PasswordPolicy = pool.policies?.passwordPolicy ?? DEFAULT_POLICY

  function startEdit() {
    setDraft({ ...current })
    setEditing(true)
  }

  function cancelEdit() {
    setEditing(false)
  }

  const updateMut = useMutation({
    ...updatePoolPasswordPolicyMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.pool(poolId) })
      toast({ title: "Password policy updated" })
      setEditing(false)
    },
    onError: () => {
      toast({ title: "Failed to update password policy", variant: "danger" })
    },
  })

  const requirementRows: { key: keyof PasswordPolicy; label: string }[] = [
    { key: "requireUppercase", label: "Require uppercase" },
    { key: "requireLowercase", label: "Require lowercase" },
    { key: "requireNumbers", label: "Require numbers" },
    { key: "requireSymbols", label: "Require symbols" },
  ]

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <ShieldCheck className="h-4 w-4 text-fg-muted" />
            <h3 className="font-mono text-sm font-medium">Password Policy</h3>
          </div>
          {!editing && (
            <Button variant="ghost" size="sm" onClick={startEdit}>
              <Pencil className="mr-1.5 h-3.5 w-3.5" /> Edit
            </Button>
          )}
        </div>

        {editing ? (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              updateMut.mutate({ poolId, policy: draft })
            }}
          >
            <div className="flex items-center gap-3">
              <label className={cn(fieldLabel, "w-48 shrink-0 text-fg-muted")}>
                Minimum length
              </label>
              <Input
                type="number"
                min={6}
                max={99}
                value={draft.minimumLength}
                onChange={(e) => setDraft((d) => ({ ...d, minimumLength: Number(e.target.value) }))}
                className="w-24"
              />
            </div>
            {requirementRows.map(({ key, label }) => (
              <label key={key} className="flex cursor-pointer items-center gap-3 text-xs">
                <input
                  type="checkbox"
                  checked={draft[key] as boolean}
                  onChange={(e) => setDraft((d) => ({ ...d, [key]: e.target.checked }))}
                  className="h-4 w-4 rounded accent-accent"
                />
                <span className="text-sm">{label}</span>
              </label>
            ))}
            <div className="flex items-center gap-3">
              <label className={cn(fieldLabel, "w-48 shrink-0 text-fg-muted")}>
                Temp password validity (days)
              </label>
              <Input
                type="number"
                min={1}
                max={365}
                value={draft.temporaryPasswordValidityDays}
                onChange={(e) =>
                  setDraft((d) => ({
                    ...d,
                    temporaryPasswordValidityDays: Number(e.target.value),
                  }))
                }
                className="w-24"
              />
            </div>
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={updateMut.isPending}>
                {updateMut.isPending ? (
                  <span className="mr-1.5 inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent" />
                ) : (
                  <Check className="mr-1.5 h-3.5 w-3.5" />
                )}
                Save
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={cancelEdit}>
                Cancel
              </Button>
            </div>
          </form>
        ) : (
          <DefinitionList>
            <PolicyRow label="Minimum length" value={String(current.minimumLength)} />
            <PolicyRow label="Require uppercase" value={current.requireUppercase ? "Yes" : "No"} />
            <PolicyRow label="Require lowercase" value={current.requireLowercase ? "Yes" : "No"} />
            <PolicyRow label="Require numbers" value={current.requireNumbers ? "Yes" : "No"} />
            <PolicyRow label="Require symbols" value={current.requireSymbols ? "Yes" : "No"} />
            <PolicyRow
              label="Temp password validity"
              value={formatQuantity(current.temporaryPasswordValidityDays, "day")}
            />
          </DefinitionList>
        )}
      </CardContent>
    </Card>
  )
}

function PolicyRow({ label, value }: { label: string; value: string }) {
  return <Definition label={label} value={value} />
}

const EMPTY_BRANDING: ManagedLoginBranding = {
  LogoURL: "",
  BackgroundColor: "",
  PrimaryColor: "",
  FontFamily: "",
  CustomCSS: "",
}

/** Side-by-side branding editor + live login page preview. */
function ManagedLoginTestPanel({ poolId, onClose }: { poolId: string; onClose: () => void }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const iframeReady = useRef(false)
  const draftRef = useRef<ManagedLoginBranding>(EMPTY_BRANDING)

  const { data: clients = [], isLoading } = useQuery(cognitoClientsQueryOptions(poolId))
  const { data: poolDetail } = useQuery(cognitoPoolDetailQueryOptions(poolId))
  const { data: branding } = useQuery(cognitoBrandingQueryOptions(poolId))

  const current = branding ?? EMPTY_BRANDING
  const [draft, setDraft] = useState<ManagedLoginBranding>(EMPTY_BRANDING)
  const [draftInitialized, setDraftInitialized] = useState(false)

  useEffect(() => {
    draftRef.current = draft
  }, [draft])

  function postBranding(d: ManagedLoginBranding) {
    const iframe = iframeRef.current
    if (iframe?.contentWindow && iframeReady.current) {
      iframe.contentWindow.postMessage({ type: "branding-preview", ...d }, "*")
    }
  }

  // Initialise draft from fetched branding once. This is derived from query
  // data, so a guarded render-time adjustment avoids a cascading effect render.
  if (branding && !draftInitialized) {
    setDraftInitialized(true)
    setDraft({ ...branding })
  }

  // Push live preview on every draft change
  useEffect(() => {
    postBranding(draft)
  }, [draft])

  // Listen for iframe "ready" signal (posted by the branding script partial)
  useEffect(() => {
    function onMessage(e: MessageEvent) {
      if (e.data?.type === "branding-preview-ready") {
        iframeReady.current = true
        postBranding(draftRef.current)
      }
    }
    window.addEventListener("message", onMessage)
    return () => window.removeEventListener("message", onMessage)
  }, [])

  const testClient = useMemo(
    (): (typeof clients)[0] | undefined =>
      clients.find((c) => (c.callbackUrls ?? []).length > 0) ?? clients[0],
    [clients],
  )

  const { baseUrl } = endpointStore.get()
  const debugUri = `${baseUrl}/_overcast/cognito/user-pools/${poolId}/debug/token`
  const callbackUrl =
    testClient != null ? ((testClient.callbackUrls ?? [])[0] ?? debugUri) : debugUri

  const authorizeUrl =
    testClient != null && poolDetail?.domain != null
      ? `${baseUrl}/_overcast/cognito/user-pools/${poolId}/oauth2/authorize?client_id=${testClient.clientId}&response_type=code&scope=openid&state=test&redirect_uri=${encodeURIComponent(callbackUrl)}`
      : null

  const saveMut = useMutation({
    ...setBrandingMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.branding(poolId) })
      toast({ title: "Branding saved", variant: "success" })
    },
    onError: () => {
      toast({ title: "Failed to save branding", variant: "danger" })
    },
  })

  const statusMessage = isLoading
    ? "Loading clients…"
    : testClient == null
      ? "Create an app client first."
      : authorizeUrl == null
        ? "Configure a domain on the Overview tab to test the login flow."
        : null

  const hasChanges = useMemo(
    () => JSON.stringify(draft) !== JSON.stringify(current),
    [draft, current],
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Paintbrush className="h-4 w-4 text-fg-muted" />
          <span className="font-mono text-sm font-medium">Customize & Preview</span>
        </div>
        <Button variant="ghost" size="sm" onClick={onClose}>
          <X className="h-4 w-4" />
        </Button>
      </div>

      {statusMessage && <p className="text-sm text-fg-muted">{statusMessage}</p>}

      {authorizeUrl && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)]">
          {/* ── Branding form ── */}
          <div className="flex flex-col gap-3">
            {(
              [
                { key: "LogoURL", label: "Logo URL", placeholder: "https://example.com/logo.png" },
                { key: "BackgroundColor", label: "Background", placeholder: "#f5f5f5" },
                { key: "PrimaryColor", label: "Primary colour", placeholder: "#0073bb" },
                { key: "FontFamily", label: "Font family", placeholder: "Inter, sans-serif" },
              ] as const
            ).map(({ key, label, placeholder }) => (
              <div key={key} className="flex flex-col gap-1">
                <label className={cn(fieldLabel, "text-fg-muted")}>{label}</label>
                <div className="flex items-center gap-2">
                  {(key === "BackgroundColor" || key === "PrimaryColor") && (
                    <input
                      type="color"
                      value={draft[key] || (key === "BackgroundColor" ? "#f5f5f5" : "#0073bb")}
                      onChange={(e) => setDraft((d) => ({ ...d, [key]: e.target.value }))}
                      className="h-8 w-8 shrink-0 cursor-pointer rounded border border-border p-0.5"
                    />
                  )}
                  <Input
                    value={draft[key]}
                    onChange={(e) => setDraft((d) => ({ ...d, [key]: e.target.value }))}
                    placeholder={placeholder}
                    className="text-sm"
                  />
                </div>
              </div>
            ))}

            <div className="flex flex-col gap-1">
              <label className={cn(fieldLabel, "text-fg-muted")}>Custom CSS</label>
              <textarea
                value={draft.CustomCSS}
                onChange={(e) => setDraft((d) => ({ ...d, CustomCSS: e.target.value }))}
                placeholder=".btn { border-radius: 8px; }"
                rows={4}
                className="w-full resize-y rounded-md border border-border bg-bg px-3 py-2 font-mono text-xs text-fg placeholder:text-fg-subtle focus:ring-1 focus:ring-accent focus:outline-none"
                spellCheck={false}
              />
            </div>

            <div className="flex items-center gap-2 pt-1">
              <Button
                size="sm"
                disabled={saveMut.isPending || !hasChanges}
                onClick={() => saveMut.mutate({ poolId, branding: draft })}
              >
                {saveMut.isPending ? (
                  <Spinner className="mr-1.5 h-3.5 w-3.5" />
                ) : (
                  <Check className="mr-1.5 h-3.5 w-3.5" />
                )}
                Save branding
              </Button>
              {hasChanges && (
                <Button variant="ghost" size="sm" onClick={() => setDraft({ ...current })}>
                  Reset
                </Button>
              )}
            </div>
          </div>

          {/* ── Live preview ── */}
          <div className="overflow-hidden rounded-lg border border-border">
            <iframe
              ref={iframeRef}
              src={authorizeUrl}
              className="h-130 w-full"
              title="Managed Login Preview"
              sandbox="allow-scripts allow-same-origin allow-forms"
              onLoad={() => {
                // Reset until the new page's branding script sends "ready"
                iframeReady.current = false
              }}
            />
          </div>
        </div>
      )}
    </div>
  )
}

// ─── Self-Registration card ──────────────────────────────────────────────

const DEFAULT_ADMIN_CONFIG: AdminCreateUserConfig = {
  allowAdminCreateUserOnly: false,
  unusedAccountValidityDays: 7,
}

function SelfRegistrationCard({ pool, poolId }: { pool: PoolSummary; poolId: string }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<AdminCreateUserConfig>(DEFAULT_ADMIN_CONFIG)

  const current: AdminCreateUserConfig = pool.adminCreateUserConfig ?? DEFAULT_ADMIN_CONFIG

  function startEdit() {
    setDraft({ ...current })
    setEditing(true)
  }

  const updateMut = useMutation({
    ...updatePoolSelfRegistrationMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.pool(poolId) })
      toast({ title: "Self-registration settings updated" })
      setEditing(false)
    },
    onError: () => {
      toast({ title: "Failed to update settings", variant: "danger" })
    },
  })

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <UserCog className="h-4 w-4 text-fg-muted" />
            <h3 className="font-mono text-sm font-medium">Self-Registration</h3>
          </div>
          {!editing && (
            <Button variant="ghost" size="sm" onClick={startEdit}>
              <Pencil className="mr-1.5 h-3.5 w-3.5" /> Edit
            </Button>
          )}
        </div>

        {editing ? (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              updateMut.mutate({ poolId, config: draft })
            }}
          >
            <label className="flex cursor-pointer items-center gap-3 text-xs">
              <input
                type="checkbox"
                checked={!draft.allowAdminCreateUserOnly}
                onChange={(e) =>
                  setDraft((d) => ({ ...d, allowAdminCreateUserOnly: !e.target.checked }))
                }
                className="h-4 w-4 rounded accent-accent"
              />
              <div className="flex flex-col">
                <span className="text-sm">Allow users to sign themselves up</span>
                <span className="text-xs text-fg-muted">
                  When disabled, only admins can create users
                </span>
              </div>
            </label>
            <div className="flex items-center gap-3">
              <label className={cn(fieldLabel, "w-64 shrink-0 text-fg-muted")}>
                Unused account expiry (days)
              </label>
              <Input
                type="number"
                min={1}
                max={365}
                value={draft.unusedAccountValidityDays}
                onChange={(e) =>
                  setDraft((d) => ({
                    ...d,
                    unusedAccountValidityDays: Number(e.target.value),
                  }))
                }
                className="w-24"
              />
            </div>
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={updateMut.isPending}>
                {updateMut.isPending ? (
                  <span className="mr-1.5 inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent" />
                ) : (
                  <Check className="mr-1.5 h-3.5 w-3.5" />
                )}
                Save
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
                Cancel
              </Button>
            </div>
          </form>
        ) : (
          <DefinitionList>
            <PolicyRow
              label="Self-registration"
              value={current.allowAdminCreateUserOnly ? "Disabled (admin only)" : "Enabled"}
            />
            <PolicyRow
              label="Unused account expiry"
              value={formatQuantity(current.unusedAccountValidityDays, "day")}
            />
          </DefinitionList>
        )}
      </CardContent>
    </Card>
  )
}

// ─── Email Configuration card ────────────────────────────────────────────

const DEFAULT_EMAIL_CONFIG: EmailConfig = {
  emailSendingAccount: "",
  sourceArn: "",
  from: "",
  replyToEmailAddress: "",
}

function EmailConfigCard({ pool, poolId }: { pool: PoolSummary; poolId: string }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<EmailConfig>(DEFAULT_EMAIL_CONFIG)

  const current: EmailConfig = pool.emailConfiguration ?? DEFAULT_EMAIL_CONFIG

  function startEdit() {
    setDraft({ ...current })
    setEditing(true)
  }

  const updateMut = useMutation({
    ...updatePoolEmailConfigMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.pool(poolId) })
      toast({ title: "Email configuration updated" })
      setEditing(false)
    },
    onError: () => {
      toast({ title: "Failed to update email configuration", variant: "danger" })
    },
  })

  const emailFields: { key: keyof EmailConfig; label: string; placeholder: string }[] = [
    {
      key: "emailSendingAccount",
      label: "Sending account",
      placeholder: "COGNITO_DEFAULT or DEVELOPER",
    },
    { key: "from", label: "From address", placeholder: "noreply@example.com" },
    { key: "replyToEmailAddress", label: "Reply-to address", placeholder: "support@example.com" },
    {
      key: "sourceArn",
      label: "SES source ARN",
      placeholder: "arn:aws:ses:us-east-1:123456789:identity/example.com",
    },
  ]

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Mail className="h-4 w-4 text-fg-muted" />
            <h3 className="font-mono text-sm font-medium">Email Configuration</h3>
          </div>
          {!editing && (
            <Button variant="ghost" size="sm" onClick={startEdit}>
              <Pencil className="mr-1.5 h-3.5 w-3.5" /> Edit
            </Button>
          )}
        </div>

        {editing ? (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              updateMut.mutate({ poolId, config: draft })
            }}
          >
            {emailFields.map(({ key, label, placeholder }) => (
              <div key={key} className="flex items-center gap-3">
                <label className={cn(fieldLabel, "w-44 shrink-0 text-fg-muted")}>{label}</label>
                <Input
                  value={draft[key]}
                  onChange={(e) => setDraft((d) => ({ ...d, [key]: e.target.value }))}
                  placeholder={placeholder}
                  className="flex-1"
                />
              </div>
            ))}
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={updateMut.isPending}>
                {updateMut.isPending ? (
                  <span className="mr-1.5 inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent" />
                ) : (
                  <Check className="mr-1.5 h-3.5 w-3.5" />
                )}
                Save
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
                Cancel
              </Button>
            </div>
          </form>
        ) : (
          <DefinitionList>
            {emailFields.map(({ key, label }) => (
              <PolicyRow key={key} label={label} value={current[key]} />
            ))}
          </DefinitionList>
        )}
      </CardContent>
    </Card>
  )
}

// ─── Verification Messages card ──────────────────────────────────────────

const DEFAULT_VERIFICATION: VerificationMessageTemplate = {
  defaultEmailOption: "CONFIRM_WITH_CODE",
  emailMessage: "",
  emailSubject: "",
  smsMessage: "",
}

function VerificationMessagesCard({ pool, poolId }: { pool: PoolSummary; poolId: string }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<VerificationMessageTemplate>(DEFAULT_VERIFICATION)

  const current: VerificationMessageTemplate =
    pool.verificationMessageTemplate ?? DEFAULT_VERIFICATION

  function startEdit() {
    setDraft({ ...current })
    setEditing(true)
  }

  const updateMut = useMutation({
    ...updatePoolVerificationMessagesMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.pool(poolId) })
      toast({ title: "Verification messages updated" })
      setEditing(false)
    },
    onError: () => {
      toast({ title: "Failed to update verification messages", variant: "danger" })
    },
  })

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <MessageSquare className="h-4 w-4 text-fg-muted" />
            <h3 className="font-mono text-sm font-medium">Verification Messages</h3>
          </div>
          {!editing && (
            <Button variant="ghost" size="sm" onClick={startEdit}>
              <Pencil className="mr-1.5 h-3.5 w-3.5" /> Edit
            </Button>
          )}
        </div>

        {editing ? (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              updateMut.mutate({ poolId, template: draft })
            }}
          >
            <div className="flex items-center gap-3">
              <label className={cn(fieldLabel, "w-44 shrink-0 text-fg-muted")}>
                Verification type
              </label>
              <select
                value={draft.defaultEmailOption}
                onChange={(e) => setDraft((d) => ({ ...d, defaultEmailOption: e.target.value }))}
                className="rounded-md border border-border bg-bg px-3 py-1.5 text-sm text-fg focus:ring-1 focus:ring-accent focus:outline-none"
              >
                <option value="CONFIRM_WITH_CODE">Code</option>
                <option value="CONFIRM_WITH_LINK">Link</option>
              </select>
            </div>
            <div className="flex items-center gap-3">
              <label className={cn(fieldLabel, "w-44 shrink-0 text-fg-muted")}>Email subject</label>
              <Input
                value={draft.emailSubject}
                onChange={(e) => setDraft((d) => ({ ...d, emailSubject: e.target.value }))}
                placeholder="Your verification code"
                className="flex-1"
              />
            </div>
            <div className="flex flex-col gap-1">
              <label className={cn(fieldLabel, "text-fg-muted")}>Email message</label>
              <textarea
                value={draft.emailMessage}
                onChange={(e) => setDraft((d) => ({ ...d, emailMessage: e.target.value }))}
                placeholder="Your verification code is {####}"
                rows={3}
                className="w-full resize-y rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg placeholder:text-fg-subtle focus:ring-1 focus:ring-accent focus:outline-none"
              />
              <p className="text-xs text-fg-muted">
                Use {"{{####}}"} for the verification code placeholder
              </p>
            </div>
            <div className="flex flex-col gap-1">
              <label className={cn(fieldLabel, "text-fg-muted")}>SMS message</label>
              <textarea
                value={draft.smsMessage}
                onChange={(e) => setDraft((d) => ({ ...d, smsMessage: e.target.value }))}
                placeholder="Your verification code is {####}"
                rows={2}
                className="w-full resize-y rounded-md border border-border bg-bg px-3 py-2 text-sm text-fg placeholder:text-fg-subtle focus:ring-1 focus:ring-accent focus:outline-none"
              />
            </div>
            <div className="flex items-center gap-2">
              <Button type="submit" size="sm" disabled={updateMut.isPending}>
                {updateMut.isPending ? (
                  <span className="mr-1.5 inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-current border-t-transparent" />
                ) : (
                  <Check className="mr-1.5 h-3.5 w-3.5" />
                )}
                Save
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
                Cancel
              </Button>
            </div>
          </form>
        ) : (
          <DefinitionList>
            <PolicyRow
              label="Verification type"
              value={current.defaultEmailOption === "CONFIRM_WITH_LINK" ? "Link" : "Code"}
            />
            <PolicyRow label="Email subject" value={current.emailSubject} />
            <PolicyRow label="Email message" value={current.emailMessage} />
            <PolicyRow label="SMS message" value={current.smsMessage} />
          </DefinitionList>
        )}
      </CardContent>
    </Card>
  )
}

// ─── Users tab ────────────────────────────────────────────────────────────

function UsersTab({
  poolId,
  usernameAttributes,
}: {
  poolId: string
  usernameAttributes?: string[]
}) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [filter, setFilter] = useState("")
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<CognitoUser>()
  const [passwordTarget, setPasswordTarget] = useState<string>()
  const [detailTarget, setDetailTarget] = useState<string>()

  const {
    data: users = [],
    isLoading,
    isFetching,
    error,
    refetch,
  } = useQuery(cognitoUsersQueryOptions(poolId))

  const filtered = useMemo(
    () =>
      filter
        ? users.filter(
            (u) =>
              u.username.toLowerCase().includes(filter.toLowerCase()) ||
              (u.attributes["email"] ?? "").toLowerCase().includes(filter.toLowerCase()),
          )
        : users,
    [users, filter],
  )

  const createMut = useResourceMutation({
    options: createUserMutationOptions(),
    invalidateKeys: [cognitoKeys.users(poolId)],
    successTitle: "User created",
    onSuccess: () => setShowCreate(false),
  })

  const deleteMut = useResourceMutation({
    options: deleteUserMutationOptions(),
    invalidateKeys: [cognitoKeys.users(poolId)],
    successTitle: "User deleted",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const disableMut = useMutation({
    ...disableUserMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.users(poolId) })
      toast({ title: "User disabled" })
    },
  })

  const enableMut = useMutation({
    ...enableUserMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.users(poolId) })
      toast({ title: "User enabled" })
    },
  })

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <div className="relative flex-1">
          <Search className="absolute top-1/2 left-2 h-3.5 w-3.5 -translate-y-1/2 text-fg-subtle" />
          <Input
            placeholder="Filter by username or email…"
            className="pl-8"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void refetch()}
          disabled={isFetching}
          title="Refresh"
        >
          <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
        </Button>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus className="mr-1 h-4 w-4" /> Create user
        </Button>
      </div>

      <ResourceTable
        variant="embedded"
        query={{ data: filtered, isLoading, error }}
        noun="users"
        emptyIcon={UserCheck}
        emptyTitle="No users"
        emptyDescription="Create a user to get started."
        emptyAction={
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="mr-1.5 h-3.5 w-3.5" /> Create user
          </Button>
        }
        errorTitle="Failed to load users"
        isFiltered={!!filter}
        onClearFilter={() => setFilter("")}
        filteredEmptyTitle="No matching users"
        filteredEmptyDescription="Try a different search."
        rowKey={(user) => user.username}
        columns={[
          {
            header: "Username",
            sortValue: (user) => user.username,
            cell: (user) => (
              <button
                className="text-left font-mono text-sm text-fg underline-offset-2 hover:text-accent hover:underline"
                onClick={() => setDetailTarget(user.username)}
              >
                {user.username}
              </button>
            ),
          },
          {
            header: "Email",
            cellClassName: "text-fg-muted",
            sortValue: (user) => user.attributes["email"] ?? "",
            cell: (user) => user.attributes["email"] ?? "—",
          },
          { header: "Status", cell: (user) => <UserStatusBadge status={user.userStatus} /> },
          {
            header: "Enabled",
            cell: (user) => (
              <Badge variant={user.enabled ? "default" : "outline"}>
                {user.enabled ? "Enabled" : "Disabled"}
              </Badge>
            ),
          },
          {
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (user) => user.userCreateDate,
            cell: (user) => formatDate(user.userCreateDate),
          },
        ]}
        rowActions={(user) => (
          <>
            <Button
              variant="ghost"
              size="sm"
              title={user.enabled ? "Disable user" : "Enable user"}
              disabled={disableMut.isPending || enableMut.isPending}
              onClick={() =>
                user.enabled
                  ? disableMut.mutate({ poolId, username: user.username })
                  : enableMut.mutate({ poolId, username: user.username })
              }
            >
              <ShieldCheck className="h-4 w-4" />
            </Button>
            <Button
              variant="ghost"
              size="sm"
              title="Set password"
              onClick={() => setPasswordTarget(user.username)}
            >
              <Settings className="h-4 w-4" />
            </Button>
          </>
        )}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (user) => ({ poolId, username: user.username }),
          label: (user) => user.username,
          noun: "user",
          title: "Delete User",
          actionLabel: (user) => `Delete user ${user.username}`,
          description: (user) => (
            <>
              Delete user <span className="font-mono font-semibold">{user.username}</span>? This
              cannot be undone.
            </>
          ),
        }}
      />

      <CreateUserDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        onSubmit={(vals) => createMut.mutate({ poolId, ...vals })}
        isPending={createMut.isPending}
        usernameAttributes={usernameAttributes}
      />

      <SetPasswordDialog
        open={!!passwordTarget}
        onOpenChange={(v) => !v && setPasswordTarget(undefined)}
        poolId={poolId}
        username={passwordTarget ?? ""}
      />

      <UserDetailDialog
        open={!!detailTarget}
        onOpenChange={(v) => !v && setDetailTarget(undefined)}
        poolId={poolId}
        username={detailTarget ?? ""}
      />
    </div>
  )
}

// ─── Groups tab ───────────────────────────────────────────────────────────

function GroupsTab({ poolId }: { poolId: string }) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()
  const [expandedGroup, setExpandedGroup] = useState<string>()

  const {
    data: groups = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(cognitoGroupsQueryOptions(poolId))

  const createMut = useResourceMutation({
    options: createGroupMutationOptions(),
    invalidateKeys: [cognitoKeys.groups(poolId)],
    successTitle: "Group created",
    onSuccess: () => setShowCreate(false),
  })

  const deleteMut = useResourceMutation({
    options: deleteGroupMutationOptions(),
    invalidateKeys: [cognitoKeys.groups(poolId)],
    successTitle: "Group deleted",
    onSuccess: () => setDeleteTarget(undefined),
  })

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-end gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void refetch()}
          disabled={isFetching}
          title="Refresh"
        >
          <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
        </Button>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus className="mr-1 h-4 w-4" /> Create group
        </Button>
      </div>

      {isLoading ? (
        <div className="flex justify-center py-12">
          <Spinner className="h-5 w-5" />
        </div>
      ) : groups.length === 0 ? (
        <EmptyState
          icon={<Users className="h-6 w-6 opacity-40" />}
          title="No groups"
          description="Create a group to organize users."
          action={
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" /> Create group
            </Button>
          }
        />
      ) : (
        <div className="flex flex-col gap-2">
          {groups.map((group) => (
            <GroupRow
              key={group.name}
              poolId={poolId}
              group={group}
              expanded={expandedGroup === group.name}
              onToggle={() =>
                setExpandedGroup((prev) => (prev === group.name ? undefined : group.name))
              }
              onDelete={() => setDeleteTarget(group.name)}
            />
          ))}
        </div>
      )}

      <CreateGroupDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        onSubmit={({ name, description }) => createMut.mutate({ poolId, name, description })}
        isPending={createMut.isPending}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(v) => !v && setDeleteTarget(undefined)}
        title="Delete Group"
        description={
          <>
            Delete group <span className="font-mono font-semibold">{deleteTarget}</span>? This
            cannot be undone.
          </>
        }
        confirmLabel="Delete"
        variant="danger"
        isPending={deleteMut.isPending}
        onConfirm={() => deleteTarget && deleteMut.mutate({ poolId, groupName: deleteTarget })}
      />
    </div>
  )
}

function GroupRow({
  poolId,
  group,
  expanded,
  onToggle,
  onDelete,
}: {
  poolId: string
  group: { name: string; description?: string; creationDate?: string }
  expanded: boolean
  onToggle: () => void
  onDelete: () => void
}) {
  const qc = useQueryClient()
  const { toast } = useToast()

  const {
    data: members = [],
    isLoading: membersLoading,
    error: membersError,
  } = useQuery({
    ...cognitoGroupMembersQueryOptions(poolId, group.name),
    enabled: expanded,
  })

  const removeMut = useMutation({
    ...removeUserFromGroupMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.groupMembers(poolId, group.name) })
      toast({ title: "User removed from group" })
    },
  })

  return (
    <Card>
      <div
        className="flex cursor-pointer items-center justify-between px-4 py-3"
        onClick={onToggle}
      >
        <div className="flex flex-col gap-0.5">
          <span className="text-sm font-medium">{group.name}</span>
          {group.description && <span className="text-xs text-fg-muted">{group.description}</span>}
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs text-fg-muted">{formatDate(group.creationDate)}</span>
          <Button
            variant="ghost"
            size="sm"
            className="text-danger hover:text-danger"
            title="Delete group"
            onClick={(e) => {
              e.stopPropagation()
              onDelete()
            }}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {expanded && (
        <div className="border-t border-border px-4 pb-3">
          <ResourceTable
            variant="embedded"
            className="pt-3"
            query={{ data: members, isLoading: membersLoading, error: membersError }}
            noun="members"
            emptyTitle="No members in this group."
            errorTitle="Failed to load group members"
            loadingCount={2}
            rowKey={(member) => member.username}
            columns={[
              {
                header: "Username",
                sortValue: (member) => member.username,
                cell: (member) => member.username,
              },
              {
                header: "Status",
                cell: (member) => <UserStatusBadge status={member.userStatus} />,
              },
            ]}
            rowActions={(member) => (
              <Button
                variant="ghost"
                size="sm"
                className="text-danger hover:text-danger"
                title="Remove from group"
                disabled={removeMut.isPending}
                onClick={() =>
                  removeMut.mutate({
                    poolId,
                    username: member.username,
                    groupName: group.name,
                  })
                }
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            )}
          />
        </div>
      )}
    </Card>
  )
}

// ─── App Clients tab ──────────────────────────────────────────────────────

function ClientsTab({ poolId }: { poolId: string }) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<{ id: string; name: string }>()
  const [newClient, setNewClient] = useState<CognitoClient>()
  const [expandedClient, setExpandedClient] = useState<string>()

  const {
    data: clients = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(cognitoClientsQueryOptions(poolId))

  const createMut = useMutation({
    ...createClientMutationOptions(),
    onSuccess: (created) => {
      void refetch()
      setShowCreate(false)
      if (created.clientSecret) {
        setNewClient(created)
      }
    },
  })

  const deleteMut = useResourceMutation({
    options: deleteClientMutationOptions(),
    invalidateKeys: [cognitoKeys.clients(poolId)],
    successTitle: "App client deleted",
    onSuccess: () => setDeleteTarget(undefined),
  })

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-end gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void refetch()}
          disabled={isFetching}
          title="Refresh"
        >
          <RefreshCw className={cn("h-4 w-4", isFetching && "animate-spin")} />
        </Button>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus className="mr-1 h-4 w-4" /> Create app client
        </Button>
      </div>

      {isLoading ? (
        <div className="flex justify-center py-12">
          <Spinner className="h-5 w-5" />
        </div>
      ) : clients.length === 0 ? (
        <EmptyState
          icon={<ShieldCheck className="h-6 w-6 opacity-40" />}
          title="No app clients"
          description="Create an app client to enable authentication."
          action={
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" /> Create app client
            </Button>
          }
        />
      ) : (
        <div className="flex flex-col gap-2">
          {clients.map((client) => (
            <Card key={client.clientId}>
              <button
                type="button"
                className="flex w-full items-center gap-3 p-4 text-left transition-colors hover:bg-accent-muted"
                onClick={() =>
                  setExpandedClient(
                    expandedClient === client.clientId ? undefined : client.clientId,
                  )
                }
              >
                {expandedClient === client.clientId ? (
                  <ChevronDown className="h-4 w-4 shrink-0 text-fg-muted" />
                ) : (
                  <ChevronRight className="h-4 w-4 shrink-0 text-fg-muted" />
                )}
                <div className="flex min-w-0 flex-1 items-center gap-4">
                  <span className="truncate font-medium">{client.clientName}</span>
                  <span className="truncate font-mono text-sm text-fg-muted">
                    {client.clientId}
                  </span>
                  {client.clientSecret && (
                    <Badge variant="outline" className="shrink-0">
                      Secret
                    </Badge>
                  )}
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="shrink-0 text-danger hover:text-danger"
                  title="Delete client"
                  onClick={(e) => {
                    e.stopPropagation()
                    setDeleteTarget({ id: client.clientId, name: client.clientName })
                  }}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </button>
              {expandedClient === client.clientId && (
                <ClientDetailPanel poolId={poolId} clientId={client.clientId} />
              )}
            </Card>
          ))}
        </div>
      )}

      <CreateClientDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        onSubmit={({ name, generateSecret }) => createMut.mutate({ poolId, name, generateSecret })}
        isPending={createMut.isPending}
      />

      {newClient && (
        <NewClientSecretDialog
          client={newClient}
          open={!!newClient}
          onClose={() => setNewClient(undefined)}
        />
      )}

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(v) => !v && setDeleteTarget(undefined)}
        title="Delete App Client"
        description={
          <>
            Delete client <span className="font-mono font-semibold">{deleteTarget?.name}</span>?
            Applications using this client will lose access.
          </>
        }
        confirmLabel="Delete"
        variant="danger"
        isPending={deleteMut.isPending}
        onConfirm={() => deleteTarget && deleteMut.mutate({ poolId, clientId: deleteTarget.id })}
      />
    </div>
  )
}

// ─── Client Detail Panel (expanded inline) ────────────────────────────────

const UNIT_OPTIONS = ["seconds", "minutes", "hours", "days"] as const

const OAUTH_FLOWS = ["code", "implicit", "client_credentials"] as const
const OAUTH_SCOPES = [
  "openid",
  "email",
  "phone",
  "profile",
  "aws.cognito.signin.user.admin",
] as const

function ClientDetailPanel({ poolId, clientId }: { poolId: string; clientId: string }) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [editing, setEditing] = useState(false)
  const [secretVisible, setSecretVisible] = useState(false)

  const { data: detail, isLoading } = useQuery(cognitoClientDetailQueryOptions(poolId, clientId))

  const [form, setForm] = useState({
    accessTokenValidity: 1,
    idTokenValidity: 1,
    refreshTokenValidity: 30,
    accessTokenUnit: "hours",
    idTokenUnit: "hours",
    refreshTokenUnit: "days",
    callbackUrls: [] as string[],
    logoutUrls: [] as string[],
    allowedOAuthFlows: [] as string[],
    allowedOAuthScopes: [] as string[],
    allowedOAuthFlowsUserPoolClient: false,
  })
  const [callbackUrlInput, setCallbackUrlInput] = useState("")
  const [logoutUrlInput, setLogoutUrlInput] = useState("")

  // Sync form from fetched data (adjust state during render pattern).
  const [prevDetail, setPrevDetail] = useState(detail)
  if (detail !== prevDetail) {
    setPrevDetail(detail)
    if (!editing && detail) {
      setForm({
        accessTokenValidity: detail.accessTokenValidity ?? 1,
        idTokenValidity: detail.idTokenValidity ?? 1,
        refreshTokenValidity: detail.refreshTokenValidity ?? 30,
        accessTokenUnit: detail.tokenValidityUnits?.accessToken ?? "hours",
        idTokenUnit: detail.tokenValidityUnits?.idToken ?? "hours",
        refreshTokenUnit: detail.tokenValidityUnits?.refreshToken ?? "days",
        callbackUrls: detail.callbackUrls ?? [],
        logoutUrls: detail.logoutUrls ?? [],
        allowedOAuthFlows: detail.allowedOAuthFlows ?? [],
        allowedOAuthScopes: detail.allowedOAuthScopes ?? [],
        allowedOAuthFlowsUserPoolClient: detail.allowedOAuthFlowsUserPoolClient ?? false,
      })
    }
  }

  const updateMut = useMutation({
    ...updateClientMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.client(poolId, clientId) })
      void qc.invalidateQueries({ queryKey: cognitoKeys.clients(poolId) })
      toast({ title: "Client settings saved" })
      setEditing(false)
    },
    onError: () => {
      toast({ title: "Failed to save client settings", variant: "danger" })
    },
  })

  function handleSave() {
    updateMut.mutate({
      poolId,
      clientId,
      updates: {
        accessTokenValidity: form.accessTokenValidity,
        idTokenValidity: form.idTokenValidity,
        refreshTokenValidity: form.refreshTokenValidity,
        tokenValidityUnits: {
          accessToken: form.accessTokenUnit,
          idToken: form.idTokenUnit,
          refreshToken: form.refreshTokenUnit,
        },
        callbackUrls: form.callbackUrls,
        logoutUrls: form.logoutUrls,
        allowedOAuthFlows: form.allowedOAuthFlows,
        allowedOAuthScopes: form.allowedOAuthScopes,
        allowedOAuthFlowsUserPoolClient: form.allowedOAuthFlowsUserPoolClient,
      },
    })
  }

  function handleCancel() {
    if (detail) {
      setForm({
        accessTokenValidity: detail.accessTokenValidity ?? 1,
        idTokenValidity: detail.idTokenValidity ?? 1,
        refreshTokenValidity: detail.refreshTokenValidity ?? 30,
        accessTokenUnit: detail.tokenValidityUnits?.accessToken ?? "hours",
        idTokenUnit: detail.tokenValidityUnits?.idToken ?? "hours",
        refreshTokenUnit: detail.tokenValidityUnits?.refreshToken ?? "days",
        callbackUrls: detail.callbackUrls ?? [],
        logoutUrls: detail.logoutUrls ?? [],
        allowedOAuthFlows: detail.allowedOAuthFlows ?? [],
        allowedOAuthScopes: detail.allowedOAuthScopes ?? [],
        allowedOAuthFlowsUserPoolClient: detail.allowedOAuthFlowsUserPoolClient ?? false,
      })
    }
    setCallbackUrlInput("")
    setLogoutUrlInput("")
    setEditing(false)
  }

  if (isLoading) {
    return (
      <div className="flex justify-center border-t px-4 py-6">
        <Spinner className="h-4 w-4" />
      </div>
    )
  }

  if (!detail) return null

  return (
    <div className="border-t bg-bg-muted/30">
      {/* Client info */}
      <DefinitionList className="px-6 pt-4 pb-4">
        <Definition label="Client ID" value={detail.clientId} copyable />
        <Definition label="Client name" value={detail.clientName} />
        {detail.clientSecret ? (
          <Definition
            label="Client secret"
            value={
              <span className="flex items-center gap-1.5">
                {secretVisible ? detail.clientSecret : "••••••••••••••••••••"}
                <IconButton
                  label={secretVisible ? "Hide secret" : "Reveal secret"}
                  onClick={() => setSecretVisible((v) => !v)}
                >
                  {secretVisible ? (
                    <EyeOff className="h-3.5 w-3.5" />
                  ) : (
                    <Eye className="h-3.5 w-3.5" />
                  )}
                </IconButton>
                {secretVisible && (
                  <CopyButton value={detail.clientSecret} noun="client secret" tone="inline" />
                )}
              </span>
            }
          />
        ) : (
          <Definition label="Client secret" value="None" />
        )}
        <Definition label="Created" value={formatDate(detail.creationDate)} />
      </DefinitionList>

      {/* Token validity section */}
      <div className="border-t border-border/50 px-6 py-4">
        <div className="mb-3 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Timer className="h-4 w-4 text-fg-muted" />
            <h4 className="font-mono text-sm font-medium">Token validity</h4>
          </div>
          {!editing ? (
            <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>
              <Pencil className="mr-1 h-3.5 w-3.5" /> Edit
            </Button>
          ) : (
            <div className="flex gap-2">
              <Button variant="ghost" size="sm" onClick={handleCancel}>
                Cancel
              </Button>
              <Button size="sm" onClick={handleSave} disabled={updateMut.isPending}>
                {updateMut.isPending ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
                Save
              </Button>
            </div>
          )}
        </div>

        {editing ? (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
            <TokenValidityField
              label="Access token"
              value={form.accessTokenValidity}
              unit={form.accessTokenUnit}
              onValueChange={(v) => setForm((f) => ({ ...f, accessTokenValidity: v }))}
              onUnitChange={(u) => setForm((f) => ({ ...f, accessTokenUnit: u }))}
            />
            <TokenValidityField
              label="ID token"
              value={form.idTokenValidity}
              unit={form.idTokenUnit}
              onValueChange={(v) => setForm((f) => ({ ...f, idTokenValidity: v }))}
              onUnitChange={(u) => setForm((f) => ({ ...f, idTokenUnit: u }))}
            />
            <TokenValidityField
              label="Refresh token"
              value={form.refreshTokenValidity}
              unit={form.refreshTokenUnit}
              onValueChange={(v) => setForm((f) => ({ ...f, refreshTokenValidity: v }))}
              onUnitChange={(u) => setForm((f) => ({ ...f, refreshTokenUnit: u }))}
            />
          </div>
        ) : (
          <DefinitionList>
            <Definition
              label="Access token"
              value={formatTokenValidity(
                detail.accessTokenValidity ?? 1,
                detail.tokenValidityUnits?.accessToken ?? "hours",
              )}
            />
            <Definition
              label="ID token"
              value={formatTokenValidity(
                detail.idTokenValidity ?? 1,
                detail.tokenValidityUnits?.idToken ?? "hours",
              )}
            />
            <Definition
              label="Refresh token"
              value={formatTokenValidity(
                detail.refreshTokenValidity ?? 30,
                detail.tokenValidityUnits?.refreshToken ?? "days",
              )}
            />
          </DefinitionList>
        )}
      </div>

      {/* OAuth / Hosted UI section */}
      <div className="border-t border-border/50 px-6 py-4">
        <div className="mb-3 flex items-center gap-2">
          <Globe className="h-4 w-4 text-fg-muted" />
          <h4 className="font-mono text-sm font-medium">OAuth 2.0 / Hosted UI</h4>
        </div>

        {editing ? (
          <div className="flex flex-col gap-4">
            {/* Callback URLs */}
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg-muted")}>Callback URLs</label>
              <div className="flex flex-col gap-1">
                {form.callbackUrls.map((url) => (
                  <div key={url} className="flex items-center gap-1.5">
                    <span className="flex-1 font-mono text-xs break-all">{url}</span>
                    <button
                      type="button"
                      className="shrink-0 text-fg-subtle hover:text-danger"
                      onClick={() =>
                        setForm((f) => ({
                          ...f,
                          callbackUrls: f.callbackUrls.filter((u) => u !== url),
                        }))
                      }
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                ))}
                <div className="flex items-center gap-1.5">
                  <Input
                    placeholder="https://example.com/callback"
                    value={callbackUrlInput}
                    onChange={(e) => setCallbackUrlInput(e.target.value)}
                    className="h-7 text-xs"
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault()
                        const v = callbackUrlInput.trim()
                        if (v && !form.callbackUrls.includes(v)) {
                          setForm((f) => ({ ...f, callbackUrls: [...f.callbackUrls, v] }))
                        }
                        setCallbackUrlInput("")
                      }
                    }}
                  />
                  <Button
                    type="button"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => {
                      const v = callbackUrlInput.trim()
                      if (v && !form.callbackUrls.includes(v)) {
                        setForm((f) => ({ ...f, callbackUrls: [...f.callbackUrls, v] }))
                      }
                      setCallbackUrlInput("")
                    }}
                  >
                    Add
                  </Button>
                </div>
              </div>
            </div>

            {/* Logout URLs */}
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg-muted")}>Sign-out URLs</label>
              <div className="flex flex-col gap-1">
                {form.logoutUrls.map((url) => (
                  <div key={url} className="flex items-center gap-1.5">
                    <span className="flex-1 font-mono text-xs break-all">{url}</span>
                    <button
                      type="button"
                      className="shrink-0 text-fg-subtle hover:text-danger"
                      onClick={() =>
                        setForm((f) => ({
                          ...f,
                          logoutUrls: f.logoutUrls.filter((u) => u !== url),
                        }))
                      }
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                ))}
                <div className="flex items-center gap-1.5">
                  <Input
                    placeholder="https://example.com/logout"
                    value={logoutUrlInput}
                    onChange={(e) => setLogoutUrlInput(e.target.value)}
                    className="h-7 text-xs"
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault()
                        const v = logoutUrlInput.trim()
                        if (v && !form.logoutUrls.includes(v)) {
                          setForm((f) => ({ ...f, logoutUrls: [...f.logoutUrls, v] }))
                        }
                        setLogoutUrlInput("")
                      }
                    }}
                  />
                  <Button
                    type="button"
                    size="sm"
                    className="h-7 px-2 text-xs"
                    onClick={() => {
                      const v = logoutUrlInput.trim()
                      if (v && !form.logoutUrls.includes(v)) {
                        setForm((f) => ({ ...f, logoutUrls: [...f.logoutUrls, v] }))
                      }
                      setLogoutUrlInput("")
                    }}
                  >
                    Add
                  </Button>
                </div>
              </div>
            </div>

            {/* OAuth flows */}
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg-muted")}>Allowed OAuth flows</label>
              <div className="flex flex-wrap gap-3">
                {OAUTH_FLOWS.map((flow) => (
                  <label key={flow} className="flex cursor-pointer items-center gap-1.5 text-xs">
                    <input
                      type="checkbox"
                      checked={form.allowedOAuthFlows.includes(flow)}
                      onChange={(e) =>
                        setForm((f) => ({
                          ...f,
                          allowedOAuthFlows: e.target.checked
                            ? [...f.allowedOAuthFlows, flow]
                            : f.allowedOAuthFlows.filter((v) => v !== flow),
                        }))
                      }
                    />
                    {flow}
                  </label>
                ))}
              </div>
              <label className="flex cursor-pointer items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  checked={form.allowedOAuthFlowsUserPoolClient}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, allowedOAuthFlowsUserPoolClient: e.target.checked }))
                  }
                />
                Allow OAuth for this client
              </label>
            </div>

            {/* OAuth scopes */}
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg-muted")}>Allowed OAuth scopes</label>
              <div className="flex flex-wrap gap-3">
                {OAUTH_SCOPES.map((scope) => (
                  <label key={scope} className="flex cursor-pointer items-center gap-1.5 text-xs">
                    <input
                      type="checkbox"
                      checked={form.allowedOAuthScopes.includes(scope)}
                      onChange={(e) =>
                        setForm((f) => ({
                          ...f,
                          allowedOAuthScopes: e.target.checked
                            ? [...f.allowedOAuthScopes, scope]
                            : f.allowedOAuthScopes.filter((v) => v !== scope),
                        }))
                      }
                    />
                    {scope}
                  </label>
                ))}
              </div>
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-3 text-sm">
            <div className="flex flex-col gap-1">
              <span className="font-mono text-xs font-medium text-fg-muted">Callback URLs</span>
              {(detail.callbackUrls ?? []).length > 0 ? (
                <div className="flex flex-col gap-0.5">
                  {(detail.callbackUrls ?? []).map((url) => (
                    <span key={url} className="font-mono text-xs">
                      {url}
                    </span>
                  ))}
                </div>
              ) : (
                <span className="font-mono text-xs text-fg-subtle">None</span>
              )}
            </div>
            <div className="flex flex-col gap-1">
              <span className="font-mono text-xs font-medium text-fg-muted">Sign-out URLs</span>
              {(detail.logoutUrls ?? []).length > 0 ? (
                <div className="flex flex-col gap-0.5">
                  {(detail.logoutUrls ?? []).map((url) => (
                    <span key={url} className="font-mono text-xs">
                      {url}
                    </span>
                  ))}
                </div>
              ) : (
                <span className="font-mono text-xs text-fg-subtle">None</span>
              )}
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="flex flex-col gap-1">
                <span className="font-mono text-xs font-medium text-fg-muted">OAuth flows</span>
                <span className="text-xs">
                  {(detail.allowedOAuthFlows ?? []).join(", ") || "—"}
                </span>
              </div>
              <div className="flex flex-col gap-1">
                <span className="font-mono text-xs font-medium text-fg-muted">OAuth scopes</span>
                <span className="text-xs">
                  {(detail.allowedOAuthScopes ?? []).join(", ") || "—"}
                </span>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function TokenValidityField({
  label,
  value,
  unit,
  onValueChange,
  onUnitChange,
}: {
  label: string
  value: number
  unit: string
  onValueChange: (v: number) => void
  onUnitChange: (u: string) => void
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label className={cn(fieldLabel, "text-fg-muted")}>{label}</label>
      <div className="flex gap-2">
        <Input
          type="number"
          min={1}
          className="w-24"
          value={value}
          onChange={(e) => onValueChange(Math.max(1, Number(e.target.value)))}
        />
        <select
          className="rounded-md border border-border bg-bg-elevated px-2.5 py-1.5 text-sm text-fg"
          value={unit}
          onChange={(e) => onUnitChange(e.target.value)}
        >
          {UNIT_OPTIONS.map((u) => (
            <option key={u} value={u}>
              {u}
            </option>
          ))}
        </select>
      </div>
    </div>
  )
}

function formatTokenValidity(value: number, unit: string): string {
  if (value === 1 && unit.endsWith("s")) {
    return `${value} ${unit.slice(0, -1)}`
  }
  return `${value} ${unit}`
}

// ─── Shared helpers ────────────────────────────────────────────────────────

/** The bare icon button the pool detail uses for its reveal affordances. */
function IconButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      className="shrink-0 text-fg-subtle transition-colors hover:text-fg"
      title={label}
    >
      {children}
    </button>
  )
}

function UserStatusBadge({ status }: { status: string }) {
  const variant: "default" | "outline" = status === "CONFIRMED" ? "default" : "outline"
  return <Badge variant={variant}>{status}</Badge>
}

// ─── User detail dialog ───────────────────────────────────────────────────

/** Well-known Cognito attributes that should not be user-editable. */
const READ_ONLY_ATTRS = new Set(["sub", "email_verified", "phone_number_verified"])

function UserDetailDialog({
  open,
  onOpenChange,
  poolId,
  username,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  poolId: string
  username: string
}) {
  const qc = useQueryClient()
  const { toast } = useToast()

  const {
    data: user,
    isLoading,
    isFetching,
    refetch,
  } = useQuery({
    ...cognitoUserDetailQueryOptions(poolId, username),
    enabled: open && !!username,
  })

  // ── Edit state ─────────────────────────────────────────────────────
  const [editingAttr, setEditingAttr] = useState<{ name: string; value: string } | null>(null)
  const [addingAttr, setAddingAttr] = useState(false)
  const [newAttrName, setNewAttrName] = useState("")
  const [newAttrValue, setNewAttrValue] = useState("")

  // ── Password reveal state ───────────────────────────────────────────
  const [plaintextPassword, setPlaintextPassword] = useState<string | null>(null)
  const [passwordVisible, setPasswordVisible] = useState(false)
  const [fetchingPassword, setFetchingPassword] = useState(false)

  async function handleRevealPassword() {
    if (plaintextPassword !== null) {
      setPasswordVisible((v) => !v)
      return
    }
    setFetchingPassword(true)
    try {
      const { cognito } = await import("@/services/api/cognito")
      const pw = await cognito.getPlaintextPassword(poolId, username)
      setPlaintextPassword(pw)
      setPasswordVisible(true)
    } finally {
      setFetchingPassword(false)
    }
  }

  const updateMut = useMutation({
    ...updateUserAttributesMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.user(poolId, username) })
      void qc.invalidateQueries({ queryKey: cognitoKeys.users(poolId) })
      toast({ title: "Attribute updated" })
      setEditingAttr(null)
    },
  })

  const deleteMut = useMutation({
    ...deleteUserAttributesMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.user(poolId, username) })
      void qc.invalidateQueries({ queryKey: cognitoKeys.users(poolId) })
      toast({ title: "Attribute deleted" })
    },
  })

  const addMut = useMutation({
    ...updateUserAttributesMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.user(poolId, username) })
      void qc.invalidateQueries({ queryKey: cognitoKeys.users(poolId) })
      toast({ title: "Attribute added" })
      setAddingAttr(false)
      setNewAttrName("")
      setNewAttrValue("")
    },
  })

  function handleClose() {
    onOpenChange(false)
    setEditingAttr(null)
    setAddingAttr(false)
    setNewAttrName("")
    setNewAttrValue("")
    setPlaintextPassword(null)
    setPasswordVisible(false)
  }

  // Sort attributes: sub first, then alphabetical.
  const sortedAttrs = useMemo(() => {
    if (!user) return []
    return Object.entries(user.attributes).sort(([a], [b]) => {
      if (a === "sub") return -1
      if (b === "sub") return 1
      return a.localeCompare(b)
    })
  }, [user])

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <DialogTitle>User — {username}</DialogTitle>
            <button
              className="shrink-0 text-fg-subtle transition-colors hover:text-fg"
              onClick={() => void refetch()}
              disabled={isFetching}
              title="Refresh"
            >
              <RefreshCw className={cn("h-3.5 w-3.5", isFetching && "animate-spin")} />
            </button>
          </div>
        </DialogHeader>

        {isLoading || !user ? (
          <div className="flex justify-center py-8">
            <Spinner className="h-5 w-5" />
          </div>
        ) : (
          <div className="flex flex-col gap-5">
            {/* ── Identity & metadata ──────────────────────────── */}
            <DefinitionList>
              <Definition label="Username" value={user.username} copyable />
              {/* An unset `sub` is an absence, so `Definition` draws the em dash
                  and withholds a copy control, rather than offering to copy one. */}
              <Definition label="Subject (sub)" value={user.attributes["sub"]} copyable />
              <Definition label="Status" value={user.userStatus} />
              <Definition label="Enabled" value={user.enabled ? "Yes" : "No"} />
              <Definition label="Created" value={formatDate(user.userCreateDate)} />
              <Definition label="Last modified" value={formatDate(user.userLastModifiedDate)} />

              {/* Password reveal */}
              <Definition
                label="Password"
                value={
                  <span className="flex items-center gap-1.5">
                    {passwordVisible && plaintextPassword
                      ? plaintextPassword
                      : plaintextPassword !== null && !passwordVisible
                        ? "••••••••"
                        : "—"}
                    <button
                      className="shrink-0 text-fg-subtle transition-colors hover:text-fg disabled:opacity-40"
                      title={passwordVisible ? "Hide password" : "Reveal password"}
                      disabled={fetchingPassword}
                      onClick={() => void handleRevealPassword()}
                    >
                      {fetchingPassword ? (
                        <Spinner className="h-3.5 w-3.5" />
                      ) : passwordVisible ? (
                        <EyeOff className="h-3.5 w-3.5" />
                      ) : (
                        <KeyRound className="h-3.5 w-3.5" />
                      )}
                    </button>
                    {passwordVisible && plaintextPassword && (
                      <CopyButton value={plaintextPassword} noun="password" tone="inline" />
                    )}
                  </span>
                }
              />
            </DefinitionList>

            {/* ── Attributes table ────────────────────────────── */}
            <div className="flex flex-col gap-2">
              <div className="flex items-center justify-between">
                <span className={cn(sectionLabel, "text-fg-muted")}>Attributes</span>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setAddingAttr(true)
                    setEditingAttr(null)
                  }}
                >
                  <Plus className="mr-1 h-3.5 w-3.5" /> Add
                </Button>
              </div>

              {/*
                ResourceTable didn't fit because this is an attribute grid, not
                a resource list: the rows are `[name, value]` pairs of one
                user's attributes, a row switches into an inline edit form on
                click, and an "add attribute" row is appended below the data.
                None of that is a query result with a row identity, so the
                loading/empty/error and row-action contracts have nothing to
                bind to. See #1327 and CONTRIBUTING § Tables.
              */}
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Value</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sortedAttrs.map(([name, value]) => (
                    <TableRow key={name}>
                      <TableCell>{name}</TableCell>
                      <TableCell>
                        {editingAttr?.name === name ? (
                          <form
                            onSubmit={(e) => {
                              e.preventDefault()
                              updateMut.mutate({
                                poolId,
                                username,
                                attributes: [{ name, value: editingAttr.value }],
                              })
                            }}
                            className="flex items-center gap-1"
                          >
                            <Input
                              value={editingAttr.value}
                              onChange={(e) => setEditingAttr({ name, value: e.target.value })}
                              className="h-7 text-xs"
                              autoFocus
                            />
                            <Button
                              type="submit"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              disabled={updateMut.isPending}
                            >
                              Save
                            </Button>
                            <Button
                              type="button"
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              onClick={() => setEditingAttr(null)}
                            >
                              Cancel
                            </Button>
                          </form>
                        ) : (
                          <div className="flex items-center gap-1.5">
                            <span className="text-xs break-all">{value}</span>
                            <CopyButton value={value} noun={name} tone="inline" />
                          </div>
                        )}
                      </TableCell>
                      <TableCell>
                        {!READ_ONLY_ATTRS.has(name) && (
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 w-7 p-0"
                              title="Edit"
                              onClick={() => setEditingAttr({ name, value })}
                            >
                              <Pencil className="h-3 w-3" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 w-7 p-0 text-danger hover:text-danger"
                              title="Delete"
                              disabled={deleteMut.isPending}
                              onClick={() =>
                                deleteMut.mutate({ poolId, username, attributeNames: [name] })
                              }
                            >
                              <Trash2 className="h-3 w-3" />
                            </Button>
                          </div>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}

                  {/* Add attribute row */}
                  {addingAttr && (
                    <TableRow>
                      <TableCell>
                        <Input
                          placeholder="custom:name"
                          value={newAttrName}
                          onChange={(e) => setNewAttrName(e.target.value)}
                          className="h-7 font-mono text-xs"
                          autoFocus
                        />
                      </TableCell>
                      <TableCell>
                        <Input
                          placeholder="value"
                          value={newAttrValue}
                          onChange={(e) => setNewAttrValue(e.target.value)}
                          className="h-7 text-xs"
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            size="sm"
                            className="h-7 px-2 text-xs"
                            disabled={!newAttrName.trim() || addMut.isPending}
                            onClick={() =>
                              addMut.mutate({
                                poolId,
                                username,
                                attributes: [{ name: newAttrName.trim(), value: newAttrValue }],
                              })
                            }
                          >
                            Add
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 px-2 text-xs"
                            onClick={() => {
                              setAddingAttr(false)
                              setNewAttrName("")
                              setNewAttrValue("")
                            }}
                          >
                            Cancel
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={handleClose}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Dialogs ───────────────────────────────────────────────────────────────

function CreateUserDialog({
  open,
  onOpenChange,
  onSubmit,
  isPending,
  usernameAttributes,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSubmit: (vals: {
    username: string
    email?: string
    phoneNumber?: string
    temporaryPassword?: string
    messageAction?: "SUPPRESS" | "RESEND"
  }) => void
  isPending: boolean
  usernameAttributes?: string[]
}) {
  const [username, setUsername] = useState("")
  const [email, setEmail] = useState("")
  const [phoneNumber, setPhoneNumber] = useState("")
  const [temporaryPassword, setTemporaryPassword] = useState("")
  const [showPassword, setShowPassword] = useState(false)
  const [suppressEmail, setSuppressEmail] = useState(false)

  const usesEmail = usernameAttributes?.includes("email") ?? false
  const usesPhone = usernameAttributes?.includes("phone_number") ?? false
  const usesPlainUsername = !usesEmail && !usesPhone

  function handleClose() {
    onOpenChange(false)
    setUsername("")
    setEmail("")
    setPhoneNumber("")
    setTemporaryPassword("")
    setShowPassword(false)
    setSuppressEmail(false)
  }

  // Determine what the primary identifier is and whether the form is valid.
  // For email/phone pools the identifier IS the username passed to AdminCreateUser.
  const primaryValue = usesPlainUsername
    ? username.trim()
    : usesEmail
      ? email.trim()
      : phoneNumber.trim()
  const canSubmit = !!primaryValue && !isPending

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create User</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (!canSubmit) return
            if (usesPlainUsername) {
              onSubmit({
                username: username.trim(),
                email: email.trim() || undefined,
                phoneNumber: phoneNumber.trim() || undefined,
                temporaryPassword: temporaryPassword || undefined,
                messageAction: suppressEmail ? "SUPPRESS" : undefined,
              })
            } else if (usesEmail && !usesPhone) {
              // Email sign-in: email is the username
              onSubmit({
                username: email.trim(),
                temporaryPassword: temporaryPassword || undefined,
                messageAction: suppressEmail ? "SUPPRESS" : undefined,
              })
            } else if (usesPhone && !usesEmail) {
              // Phone sign-in: phone is the username
              onSubmit({
                username: phoneNumber.trim(),
                email: email.trim() || undefined,
                temporaryPassword: temporaryPassword || undefined,
                messageAction: suppressEmail ? "SUPPRESS" : undefined,
              })
            } else {
              // Email or phone — use whichever is filled, prefer email
              const ident = email.trim() || phoneNumber.trim()
              onSubmit({
                username: ident,
                temporaryPassword: temporaryPassword || undefined,
                messageAction: suppressEmail ? "SUPPRESS" : undefined,
              })
            }
          }}
          className="flex flex-col gap-4"
        >
          {usesPlainUsername && (
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg")} htmlFor="new-username">
                Username
              </label>
              <Input
                id="new-username"
                placeholder="john.doe"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoFocus
              />
            </div>
          )}
          {usesEmail && (
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg")} htmlFor="new-email">
                Email address{" "}
                {usesPhone && (
                  <span className="font-normal text-fg-muted">(or use phone below)</span>
                )}
              </label>
              <Input
                id="new-email"
                type="email"
                placeholder="john@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                autoFocus
              />
              {!usesPlainUsername && !usesPhone && (
                <p className="text-xs text-fg-muted">
                  This pool uses email as the sign-in identifier. A UUID will be generated as the
                  internal username.
                </p>
              )}
            </div>
          )}
          {usesPhone && (
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg")} htmlFor="new-phone">
                Phone number{" "}
                {usesEmail && (
                  <span className="font-normal text-fg-muted">(or use email above)</span>
                )}
              </label>
              <Input
                id="new-phone"
                type="tel"
                placeholder="+15551234567"
                value={phoneNumber}
                onChange={(e) => setPhoneNumber(e.target.value)}
                autoFocus={!usesEmail}
              />
              {!usesPlainUsername && !usesEmail && (
                <p className="text-xs text-fg-muted">
                  This pool uses phone number as the sign-in identifier. A UUID will be generated as
                  the internal username.
                </p>
              )}
            </div>
          )}
          {/* Show optional email for plain-username pools that don't already have it */}
          {usesPlainUsername && (
            <div className="flex flex-col gap-1.5">
              <label className={cn(fieldLabel, "text-fg")} htmlFor="new-email">
                Email <span className="font-normal text-fg-muted">(recommended)</span>
              </label>
              <Input
                id="new-email"
                type="email"
                placeholder="john@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
              <p className="text-xs text-fg-muted">
                Required for sending the temporary password and verification emails.
              </p>
            </div>
          )}
          <div className="flex flex-col gap-1.5">
            <label className={cn(fieldLabel, "text-fg")} htmlFor="new-temp-password">
              Temporary password <span className="font-normal text-fg-muted">(optional)</span>
            </label>
            <div className="relative">
              <Input
                id="new-temp-password"
                type={showPassword ? "text" : "password"}
                placeholder="Auto-generated if empty"
                className="pr-9"
                value={temporaryPassword}
                onChange={(e) => setTemporaryPassword(e.target.value)}
              />
              <button
                type="button"
                className="absolute top-1/2 right-2.5 -translate-y-1/2 text-fg-muted hover:text-fg"
                onClick={() => setShowPassword((s) => !s)}
                tabIndex={-1}
              >
                {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={suppressEmail}
              onChange={(e) => setSuppressEmail(e.target.checked)}
              className="rounded"
            />
            Suppress invitation email
          </label>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={handleClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {isPending ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function SetPasswordDialog({
  open,
  onOpenChange,
  poolId,
  username,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  poolId: string
  username: string
}) {
  const qc = useQueryClient()
  const { toast } = useToast()
  const [password, setPassword] = useState("")
  const [permanent, setPermanent] = useState(true)
  const [show, setShow] = useState(false)

  const mut = useMutation({
    ...setUserPasswordMutationOptions(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: cognitoKeys.users(poolId) })
      toast({ title: "Password updated" })
      onOpenChange(false)
      setPassword("")
    },
  })

  function handleClose() {
    onOpenChange(false)
    setPassword("")
    setShow(false)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Set Password — {username}</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (password) mut.mutate({ poolId, username, password, permanent })
          }}
          className="flex flex-col gap-4"
        >
          <div className="flex flex-col gap-1.5">
            <label className={cn(fieldLabel, "text-fg")} htmlFor="new-password">
              New password
            </label>
            <div className="relative">
              <Input
                id="new-password"
                type={show ? "text" : "password"}
                placeholder="Min. 8 characters"
                className="pr-9"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoFocus
              />
              <button
                type="button"
                className="absolute top-1/2 right-2.5 -translate-y-1/2 text-fg-muted hover:text-fg"
                onClick={() => setShow((s) => !s)}
                tabIndex={-1}
              >
                {show ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={permanent}
              onChange={(e) => setPermanent(e.target.checked)}
              className="rounded"
            />
            Permanent (user not required to change on next sign-in)
          </label>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={handleClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!password || mut.isPending}>
              {mut.isPending ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
              Set password
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function CreateGroupDialog({
  open,
  onOpenChange,
  onSubmit,
  isPending,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSubmit: (v: { name: string; description?: string }) => void
  isPending: boolean
}) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")

  function handleClose() {
    onOpenChange(false)
    setName("")
    setDescription("")
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create Group</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (name.trim())
              onSubmit({ name: name.trim(), description: description.trim() || undefined })
          }}
          className="flex flex-col gap-4"
        >
          <div className="flex flex-col gap-1.5">
            <label className={cn(fieldLabel, "text-fg")} htmlFor="group-name">
              Group name
            </label>
            <Input
              id="group-name"
              placeholder="admins"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <label className={cn(fieldLabel, "text-fg")} htmlFor="group-desc">
              Description <span className="font-normal text-fg-muted">(optional)</span>
            </label>
            <Input
              id="group-desc"
              placeholder="A short description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={handleClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim() || isPending}>
              {isPending ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function CreateClientDialog({
  open,
  onOpenChange,
  onSubmit,
  isPending,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  onSubmit: (v: { name: string; generateSecret: boolean }) => void
  isPending: boolean
}) {
  const [name, setName] = useState("")
  const [generateSecret, setGenerateSecret] = useState(false)

  function handleClose() {
    onOpenChange(false)
    setName("")
    setGenerateSecret(false)
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create App Client</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (name.trim()) onSubmit({ name: name.trim(), generateSecret })
          }}
          className="flex flex-col gap-4"
        >
          <div className="flex flex-col gap-1.5">
            <label className={cn(fieldLabel, "text-fg")} htmlFor="client-name">
              Client name
            </label>
            <Input
              id="client-name"
              placeholder="my-app"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
            />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={generateSecret}
              onChange={(e) => setGenerateSecret(e.target.checked)}
              className="rounded"
            />
            Generate a client secret
          </label>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={handleClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim() || isPending}>
              {isPending ? <Spinner className="mr-1.5 h-3.5 w-3.5" /> : null}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function NewClientSecretDialog({
  client,
  open,
  onClose,
}: {
  client: CognitoClient
  open: boolean
  onClose: () => void
}) {
  const [show, setShow] = useState(false)

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>App Client Created</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <p className="text-sm text-fg-muted">
            Save the client secret now — it will not be shown again.
          </p>
          <div className="flex flex-col gap-1.5">
            <span className="font-mono text-xs font-medium text-fg-muted">Client ID</span>
            <span className="font-mono text-sm break-all">{client.clientId}</span>
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="font-mono text-xs font-medium text-fg-muted">Client secret</span>
            <div className="flex items-center gap-2">
              <span className="flex-1 font-mono text-sm break-all">
                {show ? client.clientSecret : "•".repeat(40)}
              </span>
              <button
                type="button"
                className="shrink-0 text-fg-muted hover:text-fg"
                onClick={() => setShow((s) => !s)}
                title={show ? "Hide" : "Show"}
              >
                {show ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
              <CopyButton
                value={client.clientSecret ?? ""}
                noun="client secret"
                tone="inline"
                className="text-fg-muted"
              />
            </div>
          </div>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
