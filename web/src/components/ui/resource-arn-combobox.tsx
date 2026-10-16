/**
 * ResourceArnCombobox — a searchable combobox that lists known AWS resources
 * of a given type and also accepts pasting any ARN directly.
 *
 * Supported service types:
 *   "sqs"              — lists all SQS queues; value = queue ARN
 *   "lambda"           — no backend list yet; free-form input with ARN placeholder
 *   "dynamodb-stream"  — lists DynamoDB tables that have streams enabled; value = stream ARN
 *   "iam-role"         — lists IAM roles; value = role ARN
 *   "esm-source"       — combined SQS queues + DynamoDB streams (for Lambda event source mappings)
 *
 * For protocols that don't use ARNs (http / https / email) use a plain <Input>
 * instead of this component.
 */
import { queryOptions, useQuery } from "@tanstack/react-query"
import type { LucideIcon } from "lucide-react"
import { Combobox } from "@/components/ui/combobox"
import { Input } from "@/components/ui/input"
import { sqsQueuesQueryOptions } from "@/features/sqs/data"
import { dynamoTablesQueryOptions } from "@/features/dynamodb/data"
import { iamRolesQueryOptions } from "@/features/iam/data"
import { SERVICES } from "@/lib/service-registry"
import { cn } from "@/lib/utils"

// ─── Types ────────────────────────────────────────────────────────────────────

export type ArnResourceType = "sqs" | "lambda" | "dynamodb-stream" | "esm-source" | "iam-role"

interface ResourceItem {
  /** Human-readable name shown in the dropdown. */
  label: string
  /** The full ARN — this becomes the combobox value when selected. */
  arn: string
  /** Optional sub-label (e.g. URL, region) shown below the name. */
  detail?: string
  /** Service identifier — used to pick an icon when items are mixed. */
  service?: "sqs" | "dynamodb" | "lambda" | "iam"
}

export interface ResourceArnComboboxProps {
  /** The AWS resource type whose ARNs should be listed as suggestions. */
  resourceType: ArnResourceType
  /** Current field value (an ARN string or empty). */
  value: string
  onChange: (value: string) => void
  id?: string
  placeholder?: string
  className?: string
  /** Optional predicate to exclude specific items from the dropdown list. */
  filterItems?: (item: ResourceItem) => boolean
}

// ─── Per-service data hooks ───────────────────────────────────────────────────

interface ResourceData {
  items: ResourceItem[]
  isPending: boolean
}

function useSqsItems(enabled: boolean): ResourceData {
  const { data = [], isPending } = useQuery({
    ...sqsQueuesQueryOptions(),
    enabled,
  })
  return {
    items: data.map((q) => ({ label: q.name, arn: q.arn, detail: q.url, service: "sqs" as const })),
    isPending: enabled && isPending,
  }
}

function useDynamoStreamItems(enabled: boolean): ResourceData {
  const { data = [], isPending } = useQuery({
    ...dynamoTablesQueryOptions(),
    enabled,
    select: (tables) => tables.filter((t) => !!t.latestStreamArn),
  })
  return {
    items: data.map((t) => ({
      label: t.tableName,
      arn: t.latestStreamArn!,
      detail: "DynamoDB Stream",
      service: "dynamodb" as const,
    })),
    isPending: enabled && isPending,
  }
}

function useIamRoleItems(enabled: boolean): ResourceData {
  const { data = [], isPending } = useQuery({ ...iamRolesQueryOptions(), enabled })
  return {
    items: data.map((r) => ({
      label: r.RoleName ?? "",
      arn: r.Arn ?? "",
      detail: r.Path,
      service: "iam" as const,
    })),
    isPending: enabled && isPending,
  }
}

// TODO(priority:P2): replace queryFn with a real Lambda ListFunctions call once
// the backend supports it — no other changes will be needed.
function lambdaFunctionsQueryOptions() {
  return queryOptions({
    queryKey: ["lambda", "functions"],
    queryFn: (): Promise<ResourceItem[]> => Promise.resolve([]),
  })
}

function useLambdaItems(enabled: boolean): ResourceData {
  const { data = [], isPending } = useQuery({
    ...lambdaFunctionsQueryOptions(),
    enabled,
  })
  return {
    items: data,
    isPending: enabled && isPending,
  }
}

function useResourceItems(resourceType: ArnResourceType): ResourceData {
  // All hooks must be called unconditionally.
  const sqsData = useSqsItems(resourceType === "sqs" || resourceType === "esm-source")
  const lambdaData = useLambdaItems(resourceType === "lambda")
  const dynamoData = useDynamoStreamItems(
    resourceType === "dynamodb-stream" || resourceType === "esm-source",
  )
  const roleData = useIamRoleItems(resourceType === "iam-role")

  if (resourceType === "sqs") return sqsData
  if (resourceType === "lambda") return lambdaData
  if (resourceType === "dynamodb-stream") return dynamoData
  if (resourceType === "iam-role") return roleData
  return {
    items: [...sqsData.items, ...dynamoData.items],
    isPending: sqsData.isPending || dynamoData.isPending,
  }
}

// ─── Per-resource config ──────────────────────────────────────────────────────

const RESOURCE_CONFIG: Record<ArnResourceType, { placeholder: string; emptyMessage: string }> = {
  sqs: {
    placeholder: "arn:aws:sqs:us-east-1:000000000000:my-queue",
    emptyMessage: "No SQS queues found — create one first.",
  },
  lambda: {
    placeholder: "arn:aws:lambda:us-east-1:000000000000:function:my-fn",
    emptyMessage: "No Lambda functions found — deploy one first.",
  },
  "dynamodb-stream": {
    placeholder: "arn:aws:dynamodb:us-east-1:000000000000:table/MyTable/stream/...",
    emptyMessage: "No DynamoDB tables with streams found — enable streams on a table first.",
  },
  "iam-role": {
    placeholder: "arn:aws:iam::000000000000:role/my-role",
    emptyMessage: "No IAM roles found — create one first.",
  },
  "esm-source": {
    placeholder: "arn:aws:sqs:us-east-1:000000000000:my-queue",
    emptyMessage:
      "No event sources found — create an SQS queue or enable streams on a DynamoDB table.",
  },
}

// ─── Service icon map ─────────────────────────────────────────────────────────

const SERVICE_ICONS: Record<string, { icon: LucideIcon; color: string }> = {
  sqs: { icon: SERVICES.sqs.icon, color: SERVICES.sqs.color },
  dynamodb: { icon: SERVICES.dynamodb.icon, color: SERVICES.dynamodb.color },
  lambda: { icon: SERVICES.lambda.icon, color: SERVICES.lambda.color },
}

/** Resource types that mix items from more than one service. */
const MULTI_SERVICE_TYPES = new Set<ArnResourceType>(["esm-source"])

// ─── Component ────────────────────────────────────────────────────────────────

export function ResourceArnCombobox({
  resourceType,
  value,
  onChange,
  id,
  placeholder,
  className,
  filterItems,
}: ResourceArnComboboxProps) {
  const { items: allItems, isPending } = useResourceItems(resourceType)
  const items = filterItems ? allItems.filter(filterItems) : allItems
  const showServiceIcon = MULTI_SERVICE_TYPES.has(resourceType)

  const config = RESOURCE_CONFIG[resourceType]
  const emptyMessage = items.length === 0 ? config.emptyMessage : undefined

  if (isPending) {
    return <Input id={id} disabled placeholder="Loading…" className={className} />
  }

  return (
    <Combobox<ResourceItem>
      id={id}
      value={value}
      onChange={onChange}
      items={items}
      allowCustom
      placeholder={placeholder ?? config.placeholder}
      emptyMessage={emptyMessage}
      className={className}
      inputClassName="font-mono"
      filterFn={(item, q) => {
        const lower = q.toLowerCase()
        return item.label.toLowerCase().includes(lower) || item.arn.toLowerCase().includes(lower)
      }}
      getItemValue={(item) => item.arn}
      renderItem={(item, { selected, active }) => {
        const svcIcon = showServiceIcon && item.service ? SERVICE_ICONS[item.service] : null
        const SvcIcon = svcIcon?.icon
        return (
          <div className="flex items-center gap-2 py-0.5">
            {showServiceIcon && (
              <div className="flex w-4 shrink-0 items-center justify-center">
                {SvcIcon && (
                  <SvcIcon
                    className={cn(
                      "h-3.5 w-3.5",
                      active || selected ? "text-white/80" : svcIcon.color,
                    )}
                  />
                )}
              </div>
            )}
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className={cn(active || selected ? "font-medium text-white" : "text-fg")}>
                {item.label}
              </span>
              <span
                className={cn(
                  "truncate font-mono text-xs",
                  active || selected ? "text-white/80" : "text-fg-muted",
                )}
              >
                {item.arn.split(":").map((seg, i) =>
                  i === 2 ? (
                    <span key={i} className="font-bold">
                      {seg}
                    </span>
                  ) : (
                    <span key={i}>{i === 0 ? seg : `:${seg}`}</span>
                  ),
                )}
              </span>
            </div>
          </div>
        )
      }}
      popoverWidth="w-[460px]"
    />
  )
}
