import { describe, expect, it } from "vitest"
import { describeChoiceRule, parentContainer, parseDefinition, parseTaskResource } from "./asl"
import { DEFINITION_TEMPLATES } from "./templates"

const tour = JSON.stringify(DEFINITION_TEMPLATES[0].definition)

describe("parseDefinition", () => {
  it("reports text that is not JSON instead of throwing", () => {
    // Given: a definition cut off mid-edit
    const result = parseDefinition('{"StartAt": ')

    // Then: an error, and no model
    expect(result.model).toBeUndefined()
    expect(result.error).toMatch(/not valid JSON/)
  })

  it("reads every state, including those nested in Parallel branches and a Map processor", () => {
    // When: the tour template is parsed
    const { model } = parseDefinition(tour)

    // Then: nested states are in the model, each in its own scope
    expect(model?.states.size).toBe(11)
    expect(model?.issues).toEqual([])
    expect(model?.states.get("Pick item")?.scopeId).toBe("Pick items#items")
    expect(model?.states.get("Charge card")?.scopeId).toBe("Fulfil in parallel#1")
    expect(model?.scopes.get("Fulfil in parallel#1")?.branchIndex).toBe(1)
    expect(model && parentContainer(model, "Pick item")).toBe("Pick items")
    expect(model && parentContainer(model, "Pick items")).toBe("Fulfil in parallel")
  })

  it("labels Choice, Default and Catch transitions", () => {
    const { model } = parseDefinition(tour)
    expect(model?.states.get("Express delivery?")?.transitions).toEqual([
      { kind: "choice", to: "Fulfil in parallel", label: "$.express == true" },
      { kind: "default", to: "Wait for warehouse", label: "Default" },
    ])
    expect(model?.states.get("Fulfil in parallel")?.transitions).toContainEqual({
      kind: "catch",
      to: "Order failed",
      label: "Catch States.ALL",
    })
  })

  it("keeps drawing a broken definition and lists what is wrong with it", () => {
    // Given: a Next to nowhere, a state with no exit, and a duplicated name across branches
    const { model } = parseDefinition(
      JSON.stringify({
        StartAt: "A",
        States: {
          A: { Type: "Pass", Next: "Missing" },
          B: { Type: "Task", Resource: "x" },
          P: {
            Type: "Parallel",
            End: true,
            Branches: [{ StartAt: "A", States: { A: { Type: "Pass", End: true } } }],
          },
        },
      }),
    )

    // Then: the model exists and names each problem
    expect(model?.states.has("A")).toBe(true)
    expect(model?.issues.join("\n")).toMatch(/"Missing", which does not exist/)
    expect(model?.issues.join("\n")).toMatch(/"B" has neither Next nor End/)
    expect(model?.issues.join("\n")).toMatch(/"A" is used more than once/)
  })
})

describe("describeChoiceRule", () => {
  it("renders comparison, type-test and boolean combinators compactly", () => {
    expect(describeChoiceRule({ Variable: "$.n", NumericGreaterThanEquals: 2 })).toBe("$.n >= 2")
    expect(describeChoiceRule({ Variable: "$.a", StringEqualsPath: "$.b" })).toBe("$.a == $.b")
    expect(describeChoiceRule({ Variable: "$.x", IsPresent: false })).toBe("$.x is not present")
    expect(
      describeChoiceRule({
        And: [
          { Variable: "$.a", BooleanEquals: true },
          {
            Or: [
              { Variable: "$.b", NumericLessThan: 1 },
              { Variable: "$.c", IsNull: true },
            ],
          },
        ],
      }),
    ).toBe("$.a == true && ($.b < 1 || $.c is null)")
    expect(describeChoiceRule({ Not: { Variable: "$.s", StringMatches: "a*" } })).toBe(
      '!($.s matches "a*")',
    )
  })

  it("shows a JSONata Condition without its delimiters", () => {
    expect(describeChoiceRule({ Condition: "{% $states.input.n > 3 %}" }, "JSONata")).toBe(
      "$states.input.n > 3",
    )
  })
})

describe("parseTaskResource", () => {
  it("names the service, the action and the integration pattern", () => {
    expect(parseTaskResource("arn:aws:states:::lambda:invoke.waitForTaskToken")).toEqual({
      service: "Lambda",
      action: "invoke",
      pattern: "callback",
    })
    expect(parseTaskResource("arn:aws:states:::ecs:runTask.sync:2")).toEqual({
      service: "ECS",
      action: "runTask",
      pattern: "sync",
    })
    expect(parseTaskResource("arn:aws:lambda:us-east-1:000000000000:function:resize:live")).toEqual(
      {
        service: "Lambda",
        action: "resize",
      },
    )
    expect(parseTaskResource("arn:aws:states:us-east-1:000000000000:activity:approve")).toEqual({
      service: "Activity",
      action: "approve",
    })
  })
})
