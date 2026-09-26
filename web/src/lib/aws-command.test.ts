import { awsCommand, type AwsCall } from "./aws-command"

const ATHENA = {
  cli: "athena",
  sdkPackage: "@aws-sdk/client-athena",
  sdkClient: "AthenaClient",
}

const CALL: AwsCall = {
  service: ATHENA,
  operation: "StartQueryExecution",
  input: {
    QueryString: "SELECT 'it''s' AS x",
    QueryExecutionContext: { Catalog: "AwsDataCatalog", Database: "sales" },
    ExecutionParameters: ["1", "'a'"],
    WorkGroup: "primary",
    ClientRequestToken: undefined,
  },
}

const TARGET = { endpoint: "http://localhost:4566", region: "eu-west-1" }

describe("awsCommand", () => {
  it("writes an AWS CLI command with the endpoint, region and kebab-case parameters", () => {
    expect(awsCommand(CALL, "cli", TARGET)).toBe(
      [
        "aws athena start-query-execution \\",
        "  --endpoint-url http://localhost:4566 \\",
        "  --region eu-west-1 \\",
        `  --query-string 'SELECT '\\''it'\\'''\\''s'\\'' AS x' \\`,
        `  --query-execution-context '{"Catalog":"AwsDataCatalog","Database":"sales"}' \\`,
        `  --execution-parameters '["1","'\\''a'\\''"]' \\`,
        "  --work-group primary",
      ].join("\n"),
    )
  })

  it("writes a boto3 call with snake_case operation and Python literals", () => {
    expect(awsCommand(CALL, "boto3", TARGET)).toBe(
      [
        "import boto3",
        "",
        "client = boto3.client(",
        '    "athena",',
        '    endpoint_url="http://localhost:4566",',
        '    region_name="eu-west-1",',
        ")",
        "response = client.start_query_execution(",
        `    QueryString="SELECT 'it''s' AS x",`,
        "    QueryExecutionContext={",
        '        "Catalog": "AwsDataCatalog",',
        '        "Database": "sales",',
        "    },",
        "    ExecutionParameters=[",
        '        "1",',
        `        "'a'",`,
        "    ],",
        '    WorkGroup="primary",',
        ")",
      ].join("\n"),
    )
  })

  it("writes an SDK v3 command with the client and command imported", () => {
    const snippet = awsCommand(CALL, "sdk-v3", TARGET)
    expect(snippet).toContain(
      'import { AthenaClient, StartQueryExecutionCommand } from "@aws-sdk/client-athena"',
    )
    expect(snippet).toContain('endpoint: "http://localhost:4566",')
    expect(snippet).toContain('"WorkGroup": "primary"')
    expect(snippet).not.toContain("ClientRequestToken")
  })

  it("leaves an input with no members as a bare call", () => {
    const call = { service: ATHENA, operation: "ListWorkGroups", input: {} }
    expect(awsCommand(call, "boto3", TARGET)).toMatch(/client\.list_work_groups\(\)$/)
  })

  it("splits acronyms in operation names", () => {
    const call = { service: ATHENA, operation: "GetQueryResults", input: { MaxResults: 10 } }
    expect(awsCommand(call, "cli", TARGET)).toContain("--max-results 10")
  })
})
