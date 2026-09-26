import { awsClients } from "../aws-clients"
import {
  ListTablesCommand,
  DescribeTableCommand,
  CreateTableCommand,
  DeleteTableCommand,
  ScanCommand,
  PutItemCommand,
  DeleteItemCommand,
  UpdateItemCommand,
  QueryCommand,
  UpdateTableCommand,
  type AttributeValue,
} from "@aws-sdk/client-dynamodb"
import { apiFetch } from "./base"
import type { DynamoTable, DynamoItem, MonitorResponse } from "@/types"
import type { ChartRangeToken } from "@/features/monitoring/types"

/** Convert SDK AttributeValue → plain DynamoDB JSON for the UI (DynamoAttrValue). */
function attrToJson(attr: AttributeValue): Record<string, unknown> {
  if (attr.S !== undefined) return { S: attr.S }
  if (attr.N !== undefined) return { N: attr.N }
  if (attr.BOOL !== undefined) return { BOOL: attr.BOOL }
  if (attr.NULL !== undefined) return { NULL: attr.NULL }
  if (attr.B !== undefined) return { B: btoa(String.fromCharCode(...attr.B)) }
  if (attr.SS !== undefined) return { SS: attr.SS }
  if (attr.NS !== undefined) return { NS: attr.NS }
  if (attr.BS !== undefined) return { BS: attr.BS.map((b) => btoa(String.fromCharCode(...b))) }
  if (attr.L !== undefined) return { L: attr.L.map(attrToJson) }
  if (attr.M !== undefined) {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(attr.M)) out[k] = attrToJson(v)
    return { M: out }
  }
  return {}
}

function itemToJson(item: Record<string, AttributeValue>): Record<string, Record<string, unknown>> {
  const out: Record<string, Record<string, unknown>> = {}
  for (const [k, v] of Object.entries(item)) out[k] = attrToJson(v)
  return out
}

function describeTableToUI(
  t: {
    TableName?: string
    TableStatus?: string
    TableArn?: string
    ItemCount?: number
    TableSizeBytes?: number
    KeySchema?: { AttributeName?: string; KeyType?: string }[]
    AttributeDefinitions?: { AttributeName?: string; AttributeType?: string }[]
    BillingModeSummary?: { BillingMode?: string }
    ProvisionedThroughput?: { ReadCapacityUnits?: number; WriteCapacityUnits?: number }
    CreationDateTime?: Date
    GlobalSecondaryIndexes?: {
      IndexName?: string
      KeySchema?: { AttributeName?: string; KeyType?: string }[]
      ItemCount?: number
    }[]
    LocalSecondaryIndexes?: {
      IndexName?: string
      KeySchema?: { AttributeName?: string; KeyType?: string }[]
      ItemCount?: number
    }[]
    StreamSpecification?: { StreamEnabled?: boolean; StreamViewType?: string }
    LatestStreamArn?: string
  },
  fallbackName?: string,
): DynamoTable {
  return {
    tableName: t.TableName ?? fallbackName ?? "",
    tableStatus: t.TableStatus ?? "UNKNOWN",
    tableArn: t.TableArn ?? "",
    itemCount: t.ItemCount ?? 0,
    tableSizeBytes: t.TableSizeBytes ?? 0,
    keySchema: (t.KeySchema ?? []).map((k) => ({
      attributeName: k.AttributeName,
      keyType: k.KeyType,
    })),
    attributeDefinitions: (t.AttributeDefinitions ?? []).map((a) => ({
      attributeName: a.AttributeName,
      attributeType: a.AttributeType,
    })),
    // AWS omits BillingModeSummary for a table left on the default mode.
    billingMode: t.BillingModeSummary?.BillingMode ?? "PROVISIONED",
    provisionedThroughput:
      t.ProvisionedThroughput?.ReadCapacityUnits || t.ProvisionedThroughput?.WriteCapacityUnits
        ? {
            readCapacityUnits: t.ProvisionedThroughput.ReadCapacityUnits ?? 0,
            writeCapacityUnits: t.ProvisionedThroughput.WriteCapacityUnits ?? 0,
          }
        : undefined,
    creationDateTime: t.CreationDateTime?.toISOString() ?? "",
    globalSecondaryIndexes: (t.GlobalSecondaryIndexes ?? []).map((g) => ({
      indexName: g.IndexName,
      keySchema: (g.KeySchema ?? []).map((k) => ({
        attributeName: k.AttributeName,
        keyType: k.KeyType,
      })),
      itemCount: g.ItemCount ?? 0,
    })),
    localSecondaryIndexes: (t.LocalSecondaryIndexes ?? []).map((l) => ({
      indexName: l.IndexName,
      keySchema: (l.KeySchema ?? []).map((k) => ({
        attributeName: k.AttributeName,
        keyType: k.KeyType,
      })),
      itemCount: l.ItemCount ?? 0,
    })),
    streamSpecification: t.StreamSpecification
      ? {
          streamEnabled: t.StreamSpecification.StreamEnabled ?? false,
          streamViewType: t.StreamSpecification.StreamViewType,
        }
      : undefined,
    latestStreamArn: t.LatestStreamArn,
  }
}

export const dynamodb = {
  /** Monitor tab read-through — docs/plans/service-metrics-platform.md phase 4. */
  getMetrics: (tableName: string, range: ChartRangeToken) =>
    apiFetch<MonitorResponse>(
      `/dynamodb/tables/${encodeURIComponent(tableName)}/metrics?range=${range}`,
    ),

  listTables: async (): Promise<DynamoTable[]> => {
    const client = awsClients.dynamodb()
    const res = await client.send(new ListTablesCommand({}))
    const names = res.TableNames ?? []
    return Promise.all(
      names.map(async (name) => {
        try {
          const desc = await client.send(new DescribeTableCommand({ TableName: name }))
          return describeTableToUI(desc.Table!, name)
        } catch {
          return {
            tableName: name,
            tableStatus: "UNKNOWN",
            tableArn: "",
            itemCount: 0,
            tableSizeBytes: 0,
            keySchema: [],
            attributeDefinitions: [],
            billingMode: "PROVISIONED",
            creationDateTime: "",
            globalSecondaryIndexes: [],
            localSecondaryIndexes: [],
            streamSpecification: undefined,
            latestStreamArn: undefined,
          }
        }
      }),
    )
  },

  describeTable: async (name: string): Promise<DynamoTable> => {
    const desc = await awsClients.dynamodb().send(new DescribeTableCommand({ TableName: name }))
    return describeTableToUI(desc.Table!)
  },

  createTable: async (opts: {
    tableName: string
    hashKeyName: string
    hashKeyType: "S" | "N" | "B"
    sortKeyName?: string
    sortKeyType?: "S" | "N" | "B"
    billingMode?: "PAY_PER_REQUEST" | "PROVISIONED"
    /** Required by DynamoDB when billingMode is PROVISIONED, rejected otherwise. */
    readCapacityUnits?: number
    writeCapacityUnits?: number
  }): Promise<DynamoTable> => {
    const keySchema: { AttributeName: string; KeyType: "HASH" | "RANGE" }[] = [
      { AttributeName: opts.hashKeyName, KeyType: "HASH" },
    ]
    const attrDefs = [{ AttributeName: opts.hashKeyName, AttributeType: opts.hashKeyType }]
    if (opts.sortKeyName) {
      keySchema.push({ AttributeName: opts.sortKeyName, KeyType: "RANGE" })
      attrDefs.push({
        AttributeName: opts.sortKeyName,
        AttributeType: opts.sortKeyType ?? "S",
      })
    }
    const billingMode = opts.billingMode ?? "PAY_PER_REQUEST"
    const res = await awsClients.dynamodb().send(
      new CreateTableCommand({
        TableName: opts.tableName,
        KeySchema: keySchema,
        AttributeDefinitions: attrDefs,
        BillingMode: billingMode,
        // DynamoDB requires capacity under PROVISIONED and rejects it under
        // PAY_PER_REQUEST, so it is sent for exactly one of the two modes.
        ProvisionedThroughput:
          billingMode === "PROVISIONED"
            ? {
                ReadCapacityUnits: opts.readCapacityUnits ?? 5,
                WriteCapacityUnits: opts.writeCapacityUnits ?? 5,
              }
            : undefined,
      }),
    )
    return describeTableToUI(res.TableDescription!)
  },

  deleteTable: async (name: string) => {
    await awsClients.dynamodb().send(new DeleteTableCommand({ TableName: name }))
  },

  scanItems: async (
    tableName: string,
    opts: { limit?: number; token?: string; indexName?: string } = {},
  ): Promise<{ items: DynamoItem[]; count: number; nextToken?: string }> => {
    const res = await awsClients.dynamodb().send(
      new ScanCommand({
        TableName: tableName,
        IndexName: opts.indexName,
        Limit: opts.limit,
        ExclusiveStartKey: opts.token
          ? (JSON.parse(atob(opts.token)) as Record<string, AttributeValue>)
          : undefined,
      }),
    )
    const items = (res.Items ?? []).map(itemToJson) as DynamoItem[]
    const nextToken = res.LastEvaluatedKey ? btoa(JSON.stringify(res.LastEvaluatedKey)) : undefined
    return { items, count: res.Count ?? items.length, nextToken }
  },

  putItem: async (tableName: string, item: DynamoItem) => {
    await awsClients.dynamodb().send(
      new PutItemCommand({
        TableName: tableName,
        Item: item as unknown as Record<string, AttributeValue>,
      }),
    )
  },

  deleteItem: async (tableName: string, key: DynamoItem) => {
    await awsClients.dynamodb().send(
      new DeleteItemCommand({
        TableName: tableName,
        Key: key as unknown as Record<string, AttributeValue>,
      }),
    )
  },

  updateItem: async (tableName: string, key: DynamoItem, item: DynamoItem) => {
    const keyAttrNames = new Set(Object.keys(key))
    const setAttrs = Object.entries(item).filter(([name]) => !keyAttrNames.has(name))
    if (setAttrs.length === 0) return
    const exprNames: Record<string, string> = {}
    const exprValues: Record<string, AttributeValue> = {}
    const setParts: string[] = []
    setAttrs.forEach(([attrName, value], i) => {
      const nameKey = `#n${i}`
      const valKey = `:v${i}`
      exprNames[nameKey] = attrName
      exprValues[valKey] = value as unknown as AttributeValue
      setParts.push(`${nameKey} = ${valKey}`)
    })
    await awsClients.dynamodb().send(
      new UpdateItemCommand({
        TableName: tableName,
        Key: key as unknown as Record<string, AttributeValue>,
        UpdateExpression: `SET ${setParts.join(", ")}`,
        ExpressionAttributeNames: exprNames,
        ExpressionAttributeValues: exprValues,
      }),
    )
  },

  queryItems: async (
    tableName: string,
    opts: {
      indexName?: string
      keyConditionExpression: string
      expressionAttributeValues: DynamoItem
      expressionAttributeNames?: Record<string, string>
      filterExpression?: string
      limit?: number
      scanIndexForward?: boolean
      token?: string
    },
  ): Promise<{ items: DynamoItem[]; count: number; nextToken?: string }> => {
    const res = await awsClients.dynamodb().send(
      new QueryCommand({
        TableName: tableName,
        IndexName: opts.indexName || undefined,
        KeyConditionExpression: opts.keyConditionExpression,
        ExpressionAttributeValues: opts.expressionAttributeValues as unknown as Record<
          string,
          AttributeValue
        >,
        ExpressionAttributeNames: opts.expressionAttributeNames,
        FilterExpression: opts.filterExpression,
        Limit: opts.limit,
        ScanIndexForward: opts.scanIndexForward,
        ExclusiveStartKey: opts.token
          ? (JSON.parse(atob(opts.token)) as Record<string, AttributeValue>)
          : undefined,
      }),
    )
    const items = (res.Items ?? []).map(itemToJson) as DynamoItem[]
    const nextToken = res.LastEvaluatedKey ? btoa(JSON.stringify(res.LastEvaluatedKey)) : undefined
    return { items, count: res.Count ?? items.length, nextToken }
  },

  updateTableStream: async (
    tableName: string,
    opts: { streamEnabled: boolean; streamViewType?: string },
  ): Promise<DynamoTable> => {
    const res = await awsClients.dynamodb().send(
      new UpdateTableCommand({
        TableName: tableName,
        StreamSpecification: {
          StreamEnabled: opts.streamEnabled,
          StreamViewType: opts.streamEnabled
            ? ((opts.streamViewType ?? "NEW_AND_OLD_IMAGES") as
                "KEYS_ONLY" | "NEW_IMAGE" | "OLD_IMAGE" | "NEW_AND_OLD_IMAGES")
            : undefined,
        },
      }),
    )
    return describeTableToUI(res.TableDescription!)
  },
}
