+ [stepfunctions/athena] Task states run Athena's four optimized integrations, including `startQueryExecution.sync`.
  the others are `stopQueryExecution`, `getQueryExecution` and `getQueryResults`; any other `athena:` resource is refused at `CreateStateMachine`.
  `.sync` waits for the query and fails the Task with `States.TaskFailed` when it ends `FAILED` or `CANCELLED`; a stopped Task stops its query.
