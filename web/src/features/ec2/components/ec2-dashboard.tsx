import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { Cpu, Plus, Trash2, Play, Square, Link as LinkIcon } from "lucide-react"
import type { Ec2ElasticIp } from "@/types"
import {
  ec2InstancesQueryOptions,
  ec2VpcsQueryOptions,
  ec2SecurityGroupsQueryOptions,
  ec2ElasticIpsQueryOptions,
  ec2NatGatewaysQueryOptions,
  ec2Keys,
  runInstancesMutationOptions,
  terminateInstancesMutationOptions,
  startInstancesMutationOptions,
  stopInstancesMutationOptions,
  createVpcMutationOptions,
  deleteVpcMutationOptions,
  createSecurityGroupMutationOptions,
  deleteSecurityGroupMutationOptions,
  allocateAddressMutationOptions,
  releaseAddressMutationOptions,
  associateAddressMutationOptions,
  disassociateAddressMutationOptions,
  createNatGatewayMutationOptions,
  deleteNatGatewayMutationOptions,
} from "@/features/ec2/data"
import { useResourceMutation } from "@/hooks/use-resource-mutation"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Combobox } from "@/components/ui/combobox"
import { FormField, fieldError } from "@/components/ui/form"
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { PageHeader, Spinner } from "@/components/ui/primitives"
import { CreateAction, RefreshAction } from "@/components/ui/resource-list-page"
import { ResourceListSection } from "@/components/ui/resource-list-section"
import { ResourceTable } from "@/components/ui/resource-table"
import { Badge } from "@/components/ui/badge"
import { InstanceStateBadge } from "./instance-state-badge"
import { Tabs, TabList, Tab, TabPanel } from "@/components/ui/tabs"
import { ServiceDocsButton, useDocsFromHash } from "@/features/docs/service-docs-modal"
import { useForm } from "@tanstack/react-form"
import { z } from "zod"
import { fieldLabel } from "@/lib/typography"
import { cn } from "@/lib/utils"
import { SegmentedControl, type SegmentedOption } from "@/components/ui/segmented-control"

export function Ec2Dashboard() {
  const [activeTab, setActiveTab] = useState("instances")
  const [docsOpen, openDocs, closeDocs] = useDocsFromHash()

  return (
    <div className="flex w-full flex-col gap-4">
      <PageHeader
        title="EC2 / VPC"
        description="Instances, VPCs, and Security Groups"
        actions={
          <ServiceDocsButton
            service="ec2"
            label="EC2 / VPC"
            open={docsOpen}
            onOpen={openDocs}
            onClose={closeDocs}
          />
        }
      />

      <Tabs selectedKey={activeTab} onSelectionChange={setActiveTab}>
        <TabList>
          <Tab id="instances">Instances</Tab>
          <Tab id="vpcs">VPCs</Tab>
          <Tab id="security-groups">Security Groups</Tab>
          <Tab id="elastic-ips">Elastic IPs</Tab>
          <Tab id="nat-gateways">NAT Gateways</Tab>
        </TabList>

        <TabPanel id="instances" className="pt-4">
          <InstancesPanel />
        </TabPanel>
        <TabPanel id="vpcs" className="pt-4">
          <VpcsPanel />
        </TabPanel>
        <TabPanel id="security-groups" className="pt-4">
          <SecurityGroupsPanel />
        </TabPanel>
        <TabPanel id="elastic-ips" className="pt-4">
          <ElasticIpsPanel />
        </TabPanel>
        <TabPanel id="nat-gateways" className="pt-4">
          <NatGatewaysPanel />
        </TabPanel>
      </Tabs>
    </div>
  )
}

// ─── NAT Gateway state badge ────────────────────────────────────────────────

function NatGatewayStateBadge({ state }: { state: string }) {
  const variant =
    state === "available"
      ? "success"
      : state === "pending"
        ? "warning"
        : state === "deleting" || state === "deleted"
          ? "default"
          : state === "failed"
            ? "danger"
            : "default"
  return <Badge variant={variant}>{state}</Badge>
}

// ─── Instances Panel ──────────────────────────────────────────────────────

type StateFilter = "all" | "running" | "stopped" | "terminated"

const STATE_FILTERS = [
  { value: "all", label: "All" },
  { value: "running", label: "Running" },
  { value: "stopped", label: "Stopped" },
  { value: "terminated", label: "Terminated" },
] as const satisfies readonly SegmentedOption<StateFilter>[]

function InstancesPanel() {
  const [showLaunch, setShowLaunch] = useState(false)
  const [terminateTarget, setTerminateTarget] = useState<string>()
  const [stateFilter, setStateFilter] = useState<StateFilter>("all")

  const {
    data: instances = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(ec2InstancesQueryOptions())

  const filtered =
    stateFilter === "all" ? instances : instances.filter((i) => i.state.name === stateFilter)

  const terminateMut = useResourceMutation({
    options: terminateInstancesMutationOptions(),
    invalidateKeys: [ec2Keys.instances()],
    successTitle: "Instance terminated",
    successVariant: "default",
    onSuccess: () => setTerminateTarget(undefined),
  })

  const startMut = useResourceMutation({
    options: startInstancesMutationOptions(),
    invalidateKeys: [ec2Keys.instances()],
    successTitle: "Instance started",
  })

  const stopMut = useResourceMutation({
    options: stopInstancesMutationOptions(),
    invalidateKeys: [ec2Keys.instances()],
    successTitle: "Instance stopped",
    successVariant: "default",
  })

  const runMut = useResourceMutation({
    options: runInstancesMutationOptions(),
    invalidateKeys: [ec2Keys.instances()],
    successTitle: "Instance launched",
    onSuccess: () => setShowLaunch(false),
  })

  return (
    <ResourceListSection
      actions={
        <>
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowLaunch(true)}>Launch Instance</CreateAction>
        </>
      }
    >
      {instances.length > 0 && (
        <SegmentedControl
          label="Instance state"
          value={stateFilter}
          options={STATE_FILTERS}
          onChange={setStateFilter}
          className="self-start"
        />
      )}

      <ResourceTable
        variant="embedded"
        query={{ data: filtered, isLoading }}
        noun="instances"
        emptyIcon={Cpu}
        emptyTitle="No instances"
        emptyDescription="Launch an instance to get started."
        emptyAction={
          <CreateAction onClick={() => setShowLaunch(true)}>Launch Instance</CreateAction>
        }
        rowKey={(i) => i.instanceId}
        columns={[
          {
            header: "Instance ID",
            sortValue: (i) => i.instanceId,
            cell: (i) => (
              <Link
                to="/ec2/$instanceId"
                params={{ instanceId: i.instanceId }}
                className="text-accent hover:underline"
              >
                {i.instanceId}
              </Link>
            ),
          },
          { header: "State", cell: (i) => <InstanceStateBadge state={i.state.name} /> },
          { header: "Type", cell: (i) => i.instanceType },
          {
            header: "Private IP",
            cellClassName: "text-fg-muted",
            cell: (i) => i.privateIpAddress ?? "—",
          },
          { header: "VPC ID", cellClassName: "text-fg-muted", cell: (i) => i.vpcId ?? "—" },
          {
            header: "Launch Time",
            cellClassName: "text-fg-muted",
            sortValue: (i) => (i.launchTime ? new Date(i.launchTime) : undefined),
            cell: (i) => (i.launchTime ? new Date(i.launchTime).toLocaleString() : "—"),
          },
        ]}
        rowActions={(i) => (
          <div className="flex gap-1">
            {i.state.name === "stopped" && (
              <Button
                size="icon"
                variant="ghost"
                title="Start"
                onClick={() => startMut.mutate([i.instanceId])}
              >
                <Play className="h-3.5 w-3.5" />
              </Button>
            )}
            {i.state.name === "running" && (
              <Button
                size="icon"
                variant="ghost"
                title="Stop"
                onClick={() => stopMut.mutate([i.instanceId])}
              >
                <Square className="h-3.5 w-3.5" />
              </Button>
            )}
            {i.state.name !== "terminated" && i.state.name !== "shutting-down" && (
              <Button
                size="icon"
                variant="ghost"
                className="text-fg-muted hover:text-danger"
                title="Terminate"
                onClick={() => setTerminateTarget(i.instanceId)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            )}
          </div>
        )}
      />

      <LaunchInstanceDialog
        open={showLaunch}
        onClose={() => setShowLaunch(false)}
        isPending={runMut.isPending}
        onSubmit={(opts) => runMut.mutate(opts)}
      />

      <ConfirmDialog
        open={!!terminateTarget}
        onOpenChange={(v) => !v && setTerminateTarget(undefined)}
        title="Terminate Instance"
        description={
          <>
            Terminate instance <strong>{terminateTarget}</strong>? This cannot be undone.
          </>
        }
        isPending={terminateMut.isPending}
        onConfirm={() => terminateTarget && terminateMut.mutate([terminateTarget])}
      />
    </ResourceListSection>
  )
}

// ─── VPCs Panel ───────────────────────────────────────────────────────────

function VpcsPanel() {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()

  const { data: vpcs = [], isLoading, isFetching, refetch } = useQuery(ec2VpcsQueryOptions())

  const createMut = useResourceMutation({
    options: createVpcMutationOptions(),
    invalidateKeys: [ec2Keys.vpcs()],
    successTitle: "VPC created",
    onSuccess: () => setShowCreate(false),
  })

  const deleteMut = useResourceMutation({
    options: deleteVpcMutationOptions(),
    invalidateKeys: [ec2Keys.vpcs()],
    successTitle: "VPC deleted",
    successVariant: "default",
    onSuccess: () => setDeleteTarget(undefined),
  })

  return (
    <ResourceListSection
      actions={
        <>
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create VPC</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: vpcs, isLoading }}
        noun="VPCs"
        emptyTitle="No VPCs"
        emptyDescription="Create a VPC to set up networking."
        emptyAction={<CreateAction onClick={() => setShowCreate(true)}>Create VPC</CreateAction>}
        rowKey={(v) => v.vpcId}
        columns={[
          {
            header: "VPC ID",
            sortValue: (v) => v.vpcId,
            cell: (v) => (
              <Link
                to="/ec2/vpc/$vpcId"
                params={{ vpcId: v.vpcId }}
                className="text-accent hover:underline"
              >
                {v.vpcId}
              </Link>
            ),
          },
          { header: "CIDR", cell: (v) => v.cidrBlock },
          {
            header: "State",
            cell: (v) => (
              <Badge variant={v.state === "available" ? "success" : "warning"}>{v.state}</Badge>
            ),
          },
          { header: "Default", cell: (v) => (v.isDefault ? "Yes" : "No") },
        ]}
        rowActions={(v) =>
          !v.isDefault && (
            <Button
              size="icon"
              variant="ghost"
              className="text-fg-muted hover:text-danger"
              onClick={() => setDeleteTarget(v.vpcId)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          )
        }
      />

      <CreateVpcDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        isPending={createMut.isPending}
        onSubmit={(cidr) => createMut.mutate(cidr)}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(v) => !v && setDeleteTarget(undefined)}
        title="Delete VPC"
        description={
          <>
            Permanently delete VPC <strong>{deleteTarget}</strong>?
          </>
        }
        isPending={deleteMut.isPending}
        onConfirm={() => deleteTarget && deleteMut.mutate(deleteTarget)}
      />
    </ResourceListSection>
  )
}

// ─── Security Groups Panel ────────────────────────────────────────────────

function SecurityGroupsPanel() {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()

  const {
    data: groups = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(ec2SecurityGroupsQueryOptions())

  const createMut = useResourceMutation({
    options: createSecurityGroupMutationOptions(),
    invalidateKeys: [ec2Keys.securityGroups()],
    successTitle: "Security group created",
    onSuccess: () => setShowCreate(false),
  })

  const deleteMut = useResourceMutation({
    options: deleteSecurityGroupMutationOptions(),
    invalidateKeys: [ec2Keys.securityGroups()],
    successTitle: "Security group deleted",
    successVariant: "default",
    onSuccess: () => setDeleteTarget(undefined),
  })

  return (
    <ResourceListSection
      actions={
        <>
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create Security Group</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: groups, isLoading }}
        noun="security groups"
        emptyTitle="No security groups"
        emptyDescription="Create a security group to manage access."
        emptyAction={
          <CreateAction onClick={() => setShowCreate(true)}>Create Security Group</CreateAction>
        }
        rowKey={(sg) => sg.groupId}
        columns={[
          { header: "Group ID", sortValue: (sg) => sg.groupId, cell: (sg) => sg.groupId },
          {
            header: "Name",
            cellClassName: "font-medium",
            sortValue: (sg) => sg.groupName,
            cell: (sg) => sg.groupName,
          },
          {
            header: "Description",
            prose: true,
            cellClassName: "max-w-xs truncate",
            cell: (sg) => sg.description,
          },
          { header: "VPC ID", cellClassName: "text-fg-muted", cell: (sg) => sg.vpcId ?? "—" },
          {
            header: "Inbound Rules",
            cell: (sg) => <Badge variant="default">{sg.ipPermissions.length}</Badge>,
          },
          {
            header: "Outbound Rules",
            cell: (sg) => <Badge variant="default">{sg.ipPermissionsEgress.length}</Badge>,
          },
        ]}
        onDelete={{
          target: groups.find((sg) => sg.groupId === deleteTarget),
          onRequest: (sg) => setDeleteTarget(sg.groupId),
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (sg) => sg.groupId,
          label: (sg) => sg.groupId,
          noun: "security group",
          title: "Delete Security Group",
          description: (sg) => (
            <>
              Permanently delete security group <strong>{sg.groupId}</strong>?
            </>
          ),
          actionLabel: (sg) => `Delete ${sg.groupId}`,
        }}
      />

      <CreateSecurityGroupDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        isPending={createMut.isPending}
        onSubmit={(opts) => createMut.mutate(opts)}
      />
    </ResourceListSection>
  )
}

// ─── Launch Instance Dialog ───────────────────────────────────────────────

const launchSchema = z.object({
  imageId: z.string().min(1, "AMI ID is required"),
  instanceType: z.string().min(1, "Instance type is required"),
  count: z.number().int().min(1).max(10),
})

function LaunchInstanceDialog({
  open,
  onClose,
  isPending,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  onSubmit: (opts: {
    imageId: string
    instanceType: string
    minCount: number
    maxCount: number
  }) => void
}) {
  const form = useForm({
    validators: { onChange: launchSchema },
    defaultValues: { imageId: "ami-12345678", instanceType: "t2.micro", count: 1 },
    onSubmit: ({ value }) =>
      onSubmit({
        imageId: value.imageId,
        instanceType: value.instanceType,
        minCount: value.count,
        maxCount: value.count,
      }),
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) {
          onClose()
          form.reset()
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Launch Instance</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <DialogBody className="space-y-4">
            <form.Field name="imageId">
              {(field) => (
                <FormField
                  label="AMI ID"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="ami-12345678"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                </FormField>
              )}
            </form.Field>
            <form.Field name="instanceType">
              {(field) => (
                <FormField
                  label="Instance Type"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Combobox<{ value: string }>
                    value={field.state.value}
                    onChange={(v) => field.handleChange(v)}
                    items={[
                      { value: "t3.micro" },
                      { value: "t3.small" },
                      { value: "t3.medium" },
                      { value: "m5.large" },
                      { value: "m5.xlarge" },
                    ]}
                    filterFn={(item, q) => item.value.toLowerCase().includes(q.toLowerCase())}
                    getItemValue={(item) => item.value}
                    renderItem={(item) => item.value}
                    allowCustom
                    placeholder="Select instance type…"
                  />
                </FormField>
              )}
            </form.Field>
            <form.Field name="count">
              {(field) => (
                <FormField
                  label="Count"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    type="number"
                    min={1}
                    max={10}
                    value={field.state.value}
                    onChange={(e) => field.handleChange(parseInt(e.target.value) || 1)}
                    onBlur={field.handleBlur}
                  />
                </FormField>
              )}
            </form.Field>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={isPending}>
              {isPending && <Spinner className="mr-2" />}
              Launch
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ─── Create VPC Dialog ────────────────────────────────────────────────────

function CreateVpcDialog({
  open,
  onClose,
  isPending,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  onSubmit: (cidr: string) => void
}) {
  const [cidr, setCidr] = useState("10.0.0.0/16")

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create VPC</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div>
            <label className={cn(fieldLabel, "mb-1 block text-fg")}>CIDR Block</label>
            <Input
              placeholder="10.0.0.0/16"
              value={cidr}
              onChange={(e) => setCidr(e.target.value)}
            />
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={isPending || !cidr} onClick={() => onSubmit(cidr)}>
            {isPending && <Spinner className="mr-2" />}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── Create Security Group Dialog ─────────────────────────────────────────

const sgSchema = z.object({
  groupName: z.string().min(1, "Name is required"),
  description: z.string().min(1, "Description is required"),
  vpcId: z.string(),
})

function CreateSecurityGroupDialog({
  open,
  onClose,
  isPending,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  isPending: boolean
  onSubmit: (opts: { groupName: string; description: string; vpcId?: string }) => void
}) {
  const form = useForm({
    validators: { onChange: sgSchema },
    defaultValues: { groupName: "", description: "", vpcId: "" },
    onSubmit: ({ value }) =>
      onSubmit({
        groupName: value.groupName,
        description: value.description,
        vpcId: value.vpcId || undefined,
      }),
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) {
          onClose()
          form.reset()
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create Security Group</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <DialogBody className="space-y-4">
            <form.Field name="groupName">
              {(field) => (
                <FormField
                  label="Group Name"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="my-sg"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                </FormField>
              )}
            </form.Field>
            <form.Field name="description">
              {(field) => (
                <FormField
                  label="Description"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="Security group description"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                </FormField>
              )}
            </form.Field>
            <form.Field name="vpcId">
              {(field) => (
                <FormField
                  label="VPC ID (optional)"
                  error={fieldError(field.state.meta.errors, field.state.meta.isTouched)}
                >
                  <Input
                    placeholder="vpc-xxxxxxxx"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                </FormField>
              )}
            </form.Field>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={isPending}>
              {isPending && <Spinner className="mr-2" />}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ─── Elastic IPs Panel ────────────────────────────────────────────────────

function ElasticIpsPanel() {
  const [associateTarget, setAssociateTarget] = useState<Ec2ElasticIp>()
  const [releaseTarget, setReleaseTarget] = useState<string>()

  const { data: eips = [], isLoading, isFetching, refetch } = useQuery(ec2ElasticIpsQueryOptions())

  const { data: instances = [] } = useQuery(ec2InstancesQueryOptions())

  const allocateMut = useResourceMutation({
    options: allocateAddressMutationOptions(),
    invalidateKeys: [ec2Keys.elasticIps()],
    successTitle: "Elastic IP allocated",
  })

  const releaseMut = useResourceMutation({
    options: releaseAddressMutationOptions(),
    invalidateKeys: [ec2Keys.elasticIps()],
    successTitle: "Elastic IP released",
    successVariant: "default",
    onSuccess: () => setReleaseTarget(undefined),
  })

  const associateMut = useResourceMutation({
    options: associateAddressMutationOptions(),
    invalidateKeys: [ec2Keys.elasticIps()],
    successTitle: "Address associated",
    onSuccess: () => setAssociateTarget(undefined),
  })

  const disassociateMut = useResourceMutation({
    options: disassociateAddressMutationOptions(),
    invalidateKeys: [ec2Keys.elasticIps()],
    successTitle: "Address disassociated",
    successVariant: "default",
  })

  return (
    <ResourceListSection
      actions={
        <>
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <Button
            size="sm"
            onClick={() => allocateMut.mutate(undefined)}
            disabled={allocateMut.isPending}
          >
            {allocateMut.isPending ? (
              <Spinner className="mr-2" />
            ) : (
              <Plus className="mr-1.5 h-3.5 w-3.5" />
            )}
            Allocate Address
          </Button>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: eips, isLoading }}
        noun="Elastic IPs"
        emptyTitle="No Elastic IPs"
        emptyDescription="Allocate an Elastic IP to reserve a static public address."
        emptyAction={
          <Button onClick={() => allocateMut.mutate(undefined)}>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Allocate Address
          </Button>
        }
        rowKey={(eip) => eip.allocationId}
        columns={[
          {
            header: "Allocation ID",
            sortValue: (eip) => eip.allocationId,
            cell: (eip) => eip.allocationId,
          },
          { header: "Public IP", cell: (eip) => eip.publicIp },
          { header: "Domain", cell: (eip) => <Badge variant="default">{eip.domain}</Badge> },
          {
            header: "Associated Instance",
            cellClassName: "text-fg-muted",
            cell: (eip) => eip.instanceId ?? "—",
          },
          {
            header: "Private IP",
            cellClassName: "text-fg-muted",
            cell: (eip) => eip.privateIpAddress ?? "—",
          },
        ]}
        rowActions={(eip) => (
          <div className="flex items-center gap-1">
            {eip.associationId ? (
              <Button
                size="sm"
                variant="ghost"
                className="text-xs text-fg-muted hover:text-warning"
                onClick={() => disassociateMut.mutate(eip.associationId!)}
                disabled={disassociateMut.isPending}
              >
                Disassociate
              </Button>
            ) : (
              <Button
                size="sm"
                variant="ghost"
                className="text-xs"
                onClick={() => setAssociateTarget(eip)}
              >
                <LinkIcon className="mr-1 h-3 w-3" />
                Associate
              </Button>
            )}
            <Button
              size="icon"
              variant="ghost"
              className="text-fg-muted hover:text-danger"
              onClick={() => setReleaseTarget(eip.allocationId)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        )}
      />

      {associateTarget && (
        <AssociateAddressDialog
          eip={associateTarget}
          instances={instances.filter((i) => i.state.name === "running")}
          isPending={associateMut.isPending}
          onClose={() => setAssociateTarget(undefined)}
          onSubmit={(params) => associateMut.mutate(params)}
        />
      )}

      <ConfirmDialog
        open={!!releaseTarget}
        onOpenChange={(v) => !v && setReleaseTarget(undefined)}
        title="Release Elastic IP"
        description={
          <>
            Release Elastic IP <strong>{releaseTarget}</strong>? This address will be returned to
            the AWS pool.
          </>
        }
        isPending={releaseMut.isPending}
        onConfirm={() => releaseTarget && releaseMut.mutate(releaseTarget)}
      />
    </ResourceListSection>
  )
}

function AssociateAddressDialog({
  eip,
  instances,
  isPending,
  onClose,
  onSubmit,
}: {
  eip: Ec2ElasticIp
  instances: Array<{ instanceId: string; state: { name: string } }>
  isPending: boolean
  onClose: () => void
  onSubmit: (params: { allocationId: string; instanceId: string }) => void
}) {
  const [instanceId, setInstanceId] = useState("")

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Associate Elastic IP</DialogTitle>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <p className="text-sm text-fg-muted">
            Associate <span className="font-mono text-xs">{eip.publicIp}</span> with a running
            instance.
          </p>
          <div className="flex flex-col gap-1">
            <label className={cn(fieldLabel, "text-fg-muted")}>Instance</label>
            <select
              value={instanceId}
              onChange={(e) => setInstanceId(e.target.value)}
              className="flex h-8 w-full rounded-md border border-border bg-bg px-3 py-1 text-sm text-fg focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
            >
              <option value="">Select instance…</option>
              {instances.map((i) => (
                <option key={i.instanceId} value={i.instanceId}>
                  {i.instanceId}
                </option>
              ))}
            </select>
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={!instanceId || isPending}
            onClick={() => onSubmit({ allocationId: eip.allocationId, instanceId })}
          >
            {isPending && <Spinner className="mr-2" />}
            Associate
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ─── NAT Gateways Panel ───────────────────────────────────────────────────

function NatGatewaysPanel() {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string>()

  const {
    data: natGateways = [],
    isLoading,
    isFetching,
    refetch,
  } = useQuery(ec2NatGatewaysQueryOptions())

  const createMut = useResourceMutation({
    options: createNatGatewayMutationOptions(),
    invalidateKeys: [ec2Keys.natGateways()],
    successTitle: "NAT Gateway created",
    onSuccess: () => setShowCreate(false),
  })

  const deleteMut = useResourceMutation({
    options: deleteNatGatewayMutationOptions(),
    invalidateKeys: [ec2Keys.natGateways()],
    successTitle: "NAT Gateway deleted",
    successVariant: "default",
    onSuccess: () => setDeleteTarget(undefined),
  })

  return (
    <ResourceListSection
      actions={
        <>
          <RefreshAction isFetching={isFetching} onClick={() => refetch()} />
          <CreateAction onClick={() => setShowCreate(true)}>Create NAT Gateway</CreateAction>
        </>
      }
    >
      <ResourceTable
        variant="embedded"
        query={{ data: natGateways, isLoading }}
        noun="NAT Gateways"
        emptyTitle="No NAT Gateways"
        emptyDescription="Create a NAT Gateway to allow private subnet internet access."
        emptyAction={
          <CreateAction onClick={() => setShowCreate(true)}>Create NAT Gateway</CreateAction>
        }
        rowKey={(ngw) => ngw.natGatewayId}
        columns={[
          {
            header: "NAT Gateway ID",
            sortValue: (ngw) => ngw.natGatewayId,
            cell: (ngw) => ngw.natGatewayId,
          },
          { header: "State", cell: (ngw) => <NatGatewayStateBadge state={ngw.state} /> },
          { header: "VPC ID", cellClassName: "text-fg-muted", cell: (ngw) => ngw.vpcId },
          { header: "Subnet ID", cellClassName: "text-fg-muted", cell: (ngw) => ngw.subnetId },
          {
            header: "Public IP",
            cellClassName: "text-fg-muted",
            cell: (ngw) => ngw.publicIp ?? "—",
          },
          {
            header: "Created",
            cellClassName: "text-fg-muted",
            sortValue: (ngw) => (ngw.createTime ? new Date(ngw.createTime) : undefined),
            cell: (ngw) => (ngw.createTime ? new Date(ngw.createTime).toLocaleString() : "—"),
          },
        ]}
        onDelete={{
          target: natGateways.find((ngw) => ngw.natGatewayId === deleteTarget),
          onRequest: (ngw) => setDeleteTarget(ngw.natGatewayId),
          onOpenChange: (open) => !open && setDeleteTarget(undefined),
          mutation: deleteMut,
          getVars: (ngw) => ngw.natGatewayId,
          label: (ngw) => ngw.natGatewayId,
          noun: "NAT Gateway",
          title: "Delete NAT Gateway",
          description: (ngw) => (
            <>
              Delete NAT Gateway <strong>{ngw.natGatewayId}</strong>? This may disrupt traffic from
              private subnets.
            </>
          ),
          actionLabel: (ngw) => `Delete ${ngw.natGatewayId}`,
        }}
      />

      {showCreate && (
        <CreateNatGatewayDialog
          isPending={createMut.isPending}
          onClose={() => setShowCreate(false)}
          onSubmit={(params) => createMut.mutate(params)}
        />
      )}
    </ResourceListSection>
  )
}

function CreateNatGatewayDialog({
  isPending,
  onClose,
  onSubmit,
}: {
  isPending: boolean
  onClose: () => void
  onSubmit: (params: { subnetId: string; allocationId: string }) => void
}) {
  const [subnetId, setSubnetId] = useState("")
  const [allocationId, setAllocationId] = useState("")

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Create NAT Gateway</DialogTitle>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <p className="text-sm text-fg-muted">
            A NAT Gateway must be placed in a public subnet. You need an Elastic IP from the Elastic
            IPs tab.
          </p>
          <FormField label="Subnet ID">
            <Input
              placeholder="subnet-xxxxxxxx"
              value={subnetId}
              onChange={(e) => setSubnetId(e.target.value)}
            />
          </FormField>
          <FormField label="Elastic IP Allocation ID">
            <Input
              placeholder="eipalloc-xxxxxxxx"
              value={allocationId}
              onChange={(e) => setAllocationId(e.target.value)}
            />
          </FormField>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={!subnetId || !allocationId || isPending}
            onClick={() => onSubmit({ subnetId, allocationId })}
          >
            {isPending && <Spinner className="mr-2" />}
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
