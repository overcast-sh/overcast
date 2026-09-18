/**
 * Starting points for a new state machine. The first two run on Overcast with
 * nothing else deployed, so a new user can create one, start it and watch the
 * live diagram move straight away.
 */
export interface DefinitionTemplate {
  id: string
  label: string
  description: string
  definition: object
}

export const DEFINITION_TEMPLATES: DefinitionTemplate[] = [
  {
    id: "tour",
    label: "Every state type",
    description:
      "Pass, Choice, Wait, Parallel, Map, Succeed and Fail — runs with no other resources.",
    definition: {
      Comment:
        "A tour of every state type. It needs no other resources, so start it and watch it run.",
      StartAt: "Prepare order",
      States: {
        "Prepare order": {
          Type: "Pass",
          Result: {
            orderId: "A-1001",
            express: false,
            items: [
              { sku: "apple", qty: 2 },
              { sku: "pear", qty: 1 },
              { sku: "plum", qty: 3 },
            ],
          },
          Next: "Express delivery?",
        },
        "Express delivery?": {
          Type: "Choice",
          Choices: [{ Variable: "$.express", BooleanEquals: true, Next: "Fulfil in parallel" }],
          Default: "Wait for warehouse",
        },
        "Wait for warehouse": { Type: "Wait", Seconds: 2, Next: "Fulfil in parallel" },
        "Fulfil in parallel": {
          Type: "Parallel",
          Branches: [
            {
              StartAt: "Pick items",
              States: {
                "Pick items": {
                  Type: "Map",
                  ItemsPath: "$.items",
                  MaxConcurrency: 2,
                  ItemProcessor: {
                    ProcessorConfig: { Mode: "INLINE" },
                    StartAt: "Pick item",
                    States: {
                      "Pick item": { Type: "Wait", Seconds: 1, Next: "Pack item" },
                      "Pack item": { Type: "Pass", End: true },
                    },
                  },
                  End: true,
                },
              },
            },
            {
              StartAt: "Charge card",
              States: {
                "Charge card": { Type: "Wait", Seconds: 1, Next: "Send receipt" },
                "Send receipt": { Type: "Pass", Result: "sent", End: true },
              },
            },
          ],
          ResultPath: "$.fulfilment",
          Catch: [{ ErrorEquals: ["States.ALL"], ResultPath: "$.error", Next: "Order failed" }],
          Next: "Order complete",
        },
        "Order complete": { Type: "Succeed" },
        "Order failed": {
          Type: "Fail",
          Error: "OrderFailed",
          Cause: "Fulfilment did not complete.",
        },
      },
    },
  },
  {
    id: "hello",
    label: "Hello world",
    description: "A single Pass state — the smallest machine that runs.",
    definition: {
      Comment: "Hello world",
      StartAt: "Hello",
      States: { Hello: { Type: "Pass", Result: { message: "Hello, world" }, End: true } },
    },
  },
  {
    id: "lambda",
    label: "Lambda with retry and catch",
    description:
      "Invokes a Lambda function, retries transient errors, and fails cleanly otherwise.",
    definition: {
      Comment:
        "Invoke a Lambda function with retries and a catch-all. Replace my-function with yours.",
      StartAt: "Invoke function",
      States: {
        "Invoke function": {
          Type: "Task",
          Resource: "arn:aws:states:::lambda:invoke",
          Parameters: { FunctionName: "my-function", "Payload.$": "$" },
          ResultSelector: { "result.$": "$.Payload" },
          Retry: [
            {
              ErrorEquals: ["Lambda.ServiceException", "Lambda.TooManyRequestsException"],
              IntervalSeconds: 1,
              MaxAttempts: 3,
              BackoffRate: 2,
            },
          ],
          Catch: [{ ErrorEquals: ["States.ALL"], ResultPath: "$.error", Next: "Handle failure" }],
          Next: "Done",
        },
        Done: { Type: "Succeed" },
        "Handle failure": { Type: "Fail", Error: "FunctionFailed", CausePath: "$.error.Cause" },
      },
    },
  },
]

export function templateDefinition(id: string): string {
  const template = DEFINITION_TEMPLATES.find((t) => t.id === id) ?? DEFINITION_TEMPLATES[0]
  return JSON.stringify(template.definition, null, 2)
}
