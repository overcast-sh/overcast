import { useState, useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { Users } from "lucide-react"
import {
  iamUsersQueryOptions,
  iamRolesQueryOptions,
  iamPoliciesQueryOptions,
  iamGroupsQueryOptions,
  iamGroupMembersQueryOptions,
  iamKeys,
  deleteUserMutationOptions,
  deleteRoleMutationOptions,
  deletePolicyMutationOptions,
  deleteGroupMutationOptions,
  createUserMutationOptions,
  createRoleMutationOptions,
  createPolicyMutationOptions,
  createGroupMutationOptions,
  type IamTab,
} from "@/features/iam/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { CreateAction, RefreshAction, ResourceListFilter } from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable } from "@/components/ui/resource-table"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { CreateResourceDialog } from "@/components/create-resource-dialog"
import { PolicySimulator, EnforcementNotice } from "./policy-simulator"
import { formatQuantity } from "@/lib/format"

interface FilterProps {
  /** Current filter text for whichever tab is active — owned by the route's `q` search param, see `useFilterSearchParam`. */
  filter: string
  onFilterChange: (value: string) => void
}

interface IAMPageProps extends FilterProps {
  /** Selected tab — owned by the route's `tab` search param, so the tab survives a reload/share. */
  tab: IamTab
  onTabChange: (tab: IamTab) => void
}

export function IAMPage({ tab, onTabChange, filter, onFilterChange }: IAMPageProps) {
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title="IAM"
        description="Identity and Access Management"
        actions={
          <ServiceDocsButton
            service="iam"
            label="IAM"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
        }
      />
      <EnforcementNotice />
      <Tabs selectedKey={tab} onSelectionChange={(key) => onTabChange(key as IamTab)}>
        <TabList>
          <Tab id="users">Users</Tab>
          <Tab id="roles">Roles</Tab>
          <Tab id="policies">Policies</Tab>
          <Tab id="groups">Groups</Tab>
          <Tab id="simulator">Simulator</Tab>
        </TabList>
        <TabPanel id="users">
          <UsersTab filter={filter} onFilterChange={onFilterChange} />
        </TabPanel>
        <TabPanel id="roles">
          <RolesTab filter={filter} onFilterChange={onFilterChange} />
        </TabPanel>
        <TabPanel id="policies">
          <PoliciesTab filter={filter} onFilterChange={onFilterChange} />
        </TabPanel>
        <TabPanel id="groups">
          <GroupsTab filter={filter} onFilterChange={onFilterChange} />
        </TabPanel>
        <TabPanel id="simulator">
          <PolicySimulator />
        </TabPanel>
      </Tabs>
    </div>
  )
}

// ─── Users Tab ─────────────────────────────────────────────────────────────

function UsersTab({ filter, onFilterChange }: FilterProps) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()
  const {
    data: users = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(iamUsersQueryOptions())

  const deleteMut = useResourceMutation({
    options: deleteUserMutationOptions(),
    invalidateKeys: [iamKeys.users()],
    successTitle: "User deleted",
    // IAM answers DeleteConflict (409) while access keys, policies or group
    // memberships remain. The AWS message names what to clear, so it is worth
    // showing verbatim rather than behind a generic "Operation failed".
    errorTitle: "Could not delete user",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const filtered = useMemo(
    () =>
      filter
        ? users.filter((u) => (u.UserName ?? "").toLowerCase().includes(filter.toLowerCase()))
        : users,
    [users, filter],
  )

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter users…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create User</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: filtered, isLoading, error }}
        noun="users"
        emptyIcon={Users}
        emptyTitle="No users"
        emptyDescription="Create an IAM user to get started."
        emptyAction={<CreateAction onClick={() => setShowCreate(true)}>Create User</CreateAction>}
        isFiltered={!!filter}
        onClearFilter={() => onFilterChange("")}
        rowKey={(u) => u.UserName ?? ""}
        columns={[
          { header: "User Name", sortValue: (u) => u.UserName, cell: (u) => u.UserName },
          { header: "ARN", cellClassName: "text-fg-muted", cell: (u) => u.Arn },
          { header: "Path", cell: (u) => u.Path },
        ]}
        onDelete={{
          target: users.find((u) => u.UserName === deleteTarget),
          onRequest: (u) => setDeleteTarget(u.UserName),
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (u) => u.UserName ?? "",
          label: (u) => u.UserName ?? "",
          noun: "user",
          title: "Delete User",
          description: (u) => (
            <>
              Delete user <span className="font-mono font-semibold">{u.UserName}</span>? This cannot
              be undone.
            </>
          ),
          actionLabel: (u) => `Delete ${u.UserName}`,
        }}
      />

      <CreateResourceDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        title="Create User"
        label="User Name"
        placeholder="my-user"
        mutationOptions={createUserMutationOptions}
        invalidateKeys={[iamKeys.users()]}
        successTitle="User created"
      />
    </ResourceListSection>
  )
}

// ─── Roles Tab ─────────────────────────────────────────────────────────────

function RolesTab({ filter, onFilterChange }: FilterProps) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()
  const {
    data: roles = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(iamRolesQueryOptions())

  const deleteMut = useResourceMutation({
    options: deleteRoleMutationOptions(),
    invalidateKeys: [iamKeys.roles()],
    successTitle: "Role deleted",
    errorTitle: "Could not delete role",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const filtered = useMemo(
    () =>
      filter
        ? roles.filter((r) => (r.RoleName ?? "").toLowerCase().includes(filter.toLowerCase()))
        : roles,
    [roles, filter],
  )

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter roles…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create Role</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: filtered, isLoading, error }}
        noun="roles"
        emptyIcon={Users}
        emptyTitle="No roles"
        emptyDescription="Create an IAM role to get started."
        emptyAction={<CreateAction onClick={() => setShowCreate(true)}>Create Role</CreateAction>}
        isFiltered={!!filter}
        onClearFilter={() => onFilterChange("")}
        rowKey={(r) => r.RoleName ?? ""}
        columns={[
          { header: "Role Name", sortValue: (r) => r.RoleName, cell: (r) => r.RoleName },
          { header: "ARN", cellClassName: "text-fg-muted", cell: (r) => r.Arn },
          { header: "Path", cell: (r) => r.Path },
        ]}
        onDelete={{
          target: roles.find((r) => r.RoleName === deleteTarget),
          onRequest: (r) => setDeleteTarget(r.RoleName),
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (r) => r.RoleName ?? "",
          label: (r) => r.RoleName ?? "",
          noun: "role",
          title: "Delete Role",
          description: (r) => (
            <>
              Delete role <span className="font-mono font-semibold">{r.RoleName}</span>? This cannot
              be undone.
            </>
          ),
          actionLabel: (r) => `Delete ${r.RoleName}`,
        }}
      />

      <CreateResourceDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        title="Create Role"
        label="Role Name"
        placeholder="my-role"
        mutationOptions={createRoleMutationOptions}
        invalidateKeys={[iamKeys.roles()]}
        successTitle="Role created"
      />
    </ResourceListSection>
  )
}

// ─── Policies Tab ──────────────────────────────────────────────────────────

function PoliciesTab({ filter, onFilterChange }: FilterProps) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<(typeof policies)[number]>()
  const {
    data: policies = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(iamPoliciesQueryOptions())

  const deleteMut = useResourceMutation({
    options: deletePolicyMutationOptions(),
    invalidateKeys: [iamKeys.policies()],
    successTitle: "Policy deleted",
    errorTitle: "Could not delete policy",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const filtered = useMemo(
    () =>
      filter
        ? policies.filter((p) => (p.PolicyName ?? "").toLowerCase().includes(filter.toLowerCase()))
        : policies,
    [policies, filter],
  )

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter policies…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create Policy</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: filtered, isLoading, error }}
        noun="policies"
        emptyIcon={Users}
        emptyTitle="No policies"
        emptyDescription="Create an IAM policy to get started."
        emptyAction={<CreateAction onClick={() => setShowCreate(true)}>Create Policy</CreateAction>}
        isFiltered={!!filter}
        onClearFilter={() => onFilterChange("")}
        rowKey={(p) => p.PolicyName ?? ""}
        columns={[
          { header: "Policy Name", sortValue: (p) => p.PolicyName, cell: (p) => p.PolicyName },
          { header: "ARN", cellClassName: "text-fg-muted", cell: (p) => p.Arn },
          {
            header: "Attachments",
            sortValue: (p) => p.AttachmentCount,
            cell: (p) => p.AttachmentCount,
          },
        ]}
        onDelete={{
          target: deleteTarget,
          onRequest: setDeleteTarget,
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          // The API takes the policy's ARN, not its name.
          getVars: (p) => p.Arn ?? "",
          label: (p) => p.PolicyName ?? "",
          noun: "policy",
          title: "Delete Policy",
          description: (p) => (
            <>
              Delete policy <span className="font-mono font-semibold">{p.PolicyName}</span>? This
              cannot be undone.
            </>
          ),
          actionLabel: (p) => `Delete ${p.PolicyName}`,
        }}
      />

      <CreateResourceDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        title="Create Policy"
        label="Policy Name"
        placeholder="my-policy"
        mutationOptions={createPolicyMutationOptions}
        invalidateKeys={[iamKeys.policies()]}
        successTitle="Policy created"
      />
    </ResourceListSection>
  )
}

// ─── Groups Tab ────────────────────────────────────────────────────────────

/**
 * The members of one group, from GetGroup. Group membership is only visible
 * through GetGroup — ListGroups does not carry it — so this is fetched lazily
 * when a group row is expanded.
 */
function GroupMembers({ groupName }: { groupName: string }) {
  const { data: members = [], isLoading } = useQuery(iamGroupMembersQueryOptions(groupName))

  if (isLoading) {
    return (
      <div className="flex justify-center py-4">
        <Spinner className="h-4 w-4" />
      </div>
    )
  }
  if (members.length === 0) {
    return <p className="py-2 text-xs text-fg-muted">No members. Add users with AddUserToGroup.</p>
  }
  return (
    <div className="flex flex-col gap-1 py-1">
      <p className="text-xs font-medium text-fg-muted">
        {formatQuantity(members.length, "member")}
      </p>
      <ul className="flex flex-col gap-0.5">
        {members.map((m) => (
          <li key={m.UserName} className="flex items-baseline gap-2 text-xs">
            <span className="font-medium text-fg">{m.UserName}</span>
            <span className="font-mono text-fg-muted">{m.Arn}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function GroupsTab({ filter, onFilterChange }: FilterProps) {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()
  const {
    data: groups = [],
    isLoading,
    isFetching,
    refetch,
    error,
  } = useQuery(iamGroupsQueryOptions())

  const deleteMut = useResourceMutation({
    options: deleteGroupMutationOptions(),
    invalidateKeys: [iamKeys.groups()],
    successTitle: "Group deleted",
    errorTitle: "Could not delete group",
    onSuccess: () => setDeleteTarget(undefined),
  })

  const filtered = useMemo(
    () =>
      filter
        ? groups.filter((g) => (g.GroupName ?? "").toLowerCase().includes(filter.toLowerCase()))
        : groups,
    [groups, filter],
  )

  return (
    <ResourceListSection
      className="pt-4"
      actions={
        <>
          <ResourceListFilter
            value={filter}
            onChange={onFilterChange}
            placeholder="Filter groups…"
            className="flex-1"
          />
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create Group</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: filtered, isLoading, error }}
        noun="groups"
        emptyIcon={Users}
        emptyTitle="No groups"
        emptyDescription="Create an IAM group to get started."
        emptyAction={<CreateAction onClick={() => setShowCreate(true)}>Create Group</CreateAction>}
        isFiltered={!!filter}
        onClearFilter={() => onFilterChange("")}
        rowKey={(g) => g.GroupName ?? ""}
        columns={[
          {
            header: "Group Name",
            cellClassName: "font-medium text-fg",
            sortValue: (g) => g.GroupName,
            cell: (g) => g.GroupName ?? "",
          },
          { header: "ARN", cellClassName: "text-fg-muted", cell: (g) => g.Arn },
          { header: "Path", cell: (g) => g.Path },
        ]}
        onDelete={{
          target: groups.find((g) => g.GroupName === deleteTarget),
          onRequest: (g) => setDeleteTarget(g.GroupName),
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (g) => g.GroupName ?? "",
          label: (g) => g.GroupName ?? "",
          noun: "group",
          title: "Delete Group",
          description: (g) => (
            <>
              Delete group <span className="font-mono font-semibold">{g.GroupName}</span>? This
              cannot be undone.
            </>
          ),
          actionLabel: (g) => `Delete ${g.GroupName}`,
        }}
        // The members belong under their group. #1200 wave 2 had to put them
        // below the whole table because `ResourceTable` could not expand a row;
        // it can now (#1327).
        expandedContent={(g) => <GroupMembers groupName={g.GroupName ?? ""} />}
      />

      <CreateResourceDialog
        open={showCreate}
        onOpenChange={setShowCreate}
        title="Create Group"
        label="Group Name"
        placeholder="my-group"
        mutationOptions={createGroupMutationOptions}
        invalidateKeys={[iamKeys.groups()]}
        successTitle="Group created"
      />
    </ResourceListSection>
  )
}
