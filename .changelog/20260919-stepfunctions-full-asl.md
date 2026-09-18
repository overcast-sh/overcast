+ [stepfunctions] JSONata query language and workflow variables (`Assign`) in both query languages, with AWS's added JSONata functions.
  JSONata runs on a JSONata 1.5 engine; functions that exist only in JSONata 2.x fail with `States.QueryEvaluationError`
+ [stepfunctions] `.waitForTaskToken`, activities and `SendTaskSuccess`/`SendTaskFailure`/`SendTaskHeartbeat`, with `HeartbeatSeconds` enforced.
+ [stepfunctions] Distributed Map: child executions, S3 `ItemReader`, `ItemBatcher`, failure tolerance, `ResultWriter`, and the map-run API.
+ [stepfunctions] `RedriveExecution`, `TestState`, `ValidateStateMachineDefinition`, state machine versions and aliases, and paginated list operations.
+ [stepfunctions] `aws-sdk:` integrations for AWS JSON services and S3, plus `dynamodb:deleteItem` and `events:putEvents`.
+ [stepfunctions] all 18 intrinsic functions and full JSONPath paths — wildcards, descent, slices, unions and filters.
~ [stepfunctions] Parallel branches and Map iterations run concurrently, honouring `MaxConcurrency`; a failure aborts the siblings.
  history links each branch and iteration causally through `previousEventId`, and Map iteration events carry the Map state's `name`
*! [stepfunctions] a Parallel or Map failure keeps the branch's own error name instead of `States.BranchFailed`/`States.TaskFailed`.
  migration: a `Catch` or `Retry` naming `States.BranchFailed` should name the branch's error, or use `States.ALL`
*! [stepfunctions] `CreateStateMachine` rejects duplicate state names across branches and `States.ALL` outside the last `Retry`/`Catch`.
  it also rejects fields of the other query language (a JSONPath `Output`, a JSONata `InputPath`), matching AWS
  and, for EXPRESS, `.sync`, `.waitForTaskToken`, activities and distributed Map, which AWS also refuses there
  migration: rename nested states so every name is unique, and move `States.ALL` into its own final entry
* [stepfunctions] Choice type mismatches evaluate false, a Fail state without `Error` is catchable, and `ItemSelector` reads the Map input.
  `JitterStrategy: FULL` is applied, and `States.Timeout` also matches `States.HeartbeatTimeout`
+ [stepfunctions/cloudformation] `AWS::StepFunctions::Activity`, `StateMachineVersion` and `StateMachineAlias`, and `DefinitionS3Location`.
* [stepfunctions] `GetExecutionHistory` with `includeExecutionData: false` no longer blanks the payloads out of the stored history.
+ [web/stepfunctions] Step Functions pages draw each state machine as a flow diagram, with Parallel branches and Map item processors as nested lanes.
  Executions play live on it: running states pulse, taken paths light up, and each Map iteration can be viewed on its own.
  Click a state for its runs, input, output, retries and errors; export the diagram as SVG or PNG.
+ [web/stepfunctions] Execution history reads as a timeline of state runs by branch and iteration, or a filterable, searchable event list.
+ [web/stepfunctions] Create or edit a definition beside a live diagram preview, from ready-to-run templates, and stop, redrive or re-run executions.
