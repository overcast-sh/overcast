---
title: "Step Functions execution history"
description: "How GetExecutionHistory events link together, so a reader can attribute every event to its state, Parallel branch and Map iteration."
section: "Service Reference"
tags:
  - docs
  - services
  - stepfunctions
---

# Step Functions execution history

`GetExecutionHistory` returns AWS's event vocabulary, and every event names its
causal predecessor in `previousEventId`. That link, not the event order, is how
you tell which branch or iteration an event belongs to: Parallel branches and
Map iterations run concurrently, so their events interleave in `id` order.

## Attributing an event to a state

Every state records `<Type>StateEntered` when it starts and `<Type>StateExited`
when it finishes, both carrying `name`. A Fail state has no exit event; the
execution's terminal event follows it. A Task, Wait, Parallel or Map interrupted
by `StopExecution`, a timeout or a failed sibling records `<Type>StateAborted`,
which links to the state's last event.

Between entry and exit a state records its own work:

| State        | Events between entered and exited                                                                   |
| ------------ | --------------------------------------------------------------------------------------------------- |
| Task         | `TaskScheduled`, `TaskStarted`, `TaskSubmitted` (callbacks), then `TaskSucceeded`, `TaskFailed` or `TaskTimedOut` |
| Lambda ARN   | `LambdaFunctionScheduled`, `LambdaFunctionStarted`, then `…Succeeded`, `…Failed` or `…TimedOut`     |
| Activity     | `ActivityScheduled`, `ActivityStarted` (`workerName`), then `ActivitySucceeded`, `…Failed` or `…TimedOut` |
| Parallel     | `ParallelStateStarted`, the branches' events, `ParallelStateSucceeded` or `ParallelStateFailed`     |
| Inline Map   | `MapStateStarted` (`length`), the iterations' events, `MapStateSucceeded` or `MapStateFailed`       |
| Distributed Map | `MapStateStarted`, `MapRunStarted` (`mapRunArn`) — or `MapRunRedriven` on a redrive — `MapRunSucceeded` or `MapRunFailed`, then `MapStateSucceeded` or `MapStateFailed` |

A retried Task repeats its scheduled-to-failed events inside one entered/exited
pair. A JSONata expression that fails records `EvaluationFailed` with the
`state` and field (`location`) before the state fails with
`States.QueryEvaluationError`. `<Type>StateExited` carries `assignedVariables`
when the state assigned any.

## Parallel branches

Each branch's first event links to the Parallel's `ParallelStateStarted`, and
each later event links to the previous event of the same branch. Walk
`previousEventId` back from any event: the first state you meet after
`ParallelStateStarted` is the branch's `StartAt`, which names the branch —
state names are unique across the whole state machine.

```text
id  type                    previousEventId  details
2   ParallelStateEntered    1                name: Fan
3   ParallelStateStarted    2
4   PassStateEntered        3                name: A1   (branch 0)
5   PassStateEntered        3                name: B1   (branch 1)
6   PassStateExited         4                name: A1
7   PassStateExited         5                name: B1
8   ParallelStateSucceeded  7
9   ParallelStateExited     8                name: Fan
```

`ParallelStateSucceeded` or `ParallelStateFailed` links to whichever branch
event came last.

## Map iterations

Each iteration opens with `MapIterationStarted`, which links to
`MapStateStarted` and carries `mapIterationStartedEventDetails`
`{"name": "<Map state name>", "index": <n>}`. The iteration's own events chain
from it, and it closes with `MapIterationSucceeded`, `MapIterationFailed` or
`MapIterationAborted` — same `name` and `index` — linked to the iteration's
last event.

```text
id  type                   previousEventId  details
3   MapStateStarted        2                length: 2
4   MapIterationStarted    3                name: Each, index: 0
5   MapIterationStarted    3                name: Each, index: 1
6   PassStateEntered       4                name: Work
7   PassStateEntered       5                name: Work
8   PassStateExited        6                name: Work
9   MapIterationSucceeded  8                name: Each, index: 0
```

A distributed Map runs each item (or batch) as a child execution with its own
history. The parent records only the map run; list the children with
`ListExecutions` and the `mapRunArn` from `MapRunStarted`.

## Redriven executions

`RedriveExecution` appends to the history rather than starting a new one. It
records `ExecutionRedriven` (`executionRedrivenEventDetails.redriveCount`),
linked to the run's terminal event, and the state the run stopped in is
entered again with `<Type>StateEntered` linked to `ExecutionRedriven`. States
that had succeeded record nothing more.

When that state is a Parallel or an inline Map, it records its
`ParallelStateStarted` or `MapStateStarted` (with the full `length`) again,
and then only the branches and iterations that had not succeeded record
events: each one's first event is the state it stopped in, entered again, and
a redriven iteration opens with `MapIterationStarted` carrying its original
`index`. The rest contribute their earlier output silently. The same applies
inside them, so a Parallel in a Map iteration resumes only its failed branch.

```text
id  type                    previousEventId  details
9   ExecutionFailed         8
10  ExecutionRedriven       9                redriveCount: 1
11  ParallelStateEntered    10               name: Fan
12  ParallelStateStarted    11
13  TaskStateEntered        12               name: Gate   (branch 1 only)
…
17  ParallelStateSucceeded  16
18  ParallelStateExited     17               name: Fan
```

A distributed Map redrives its map run instead of starting another:
`MapStateStarted` is followed by `MapRunRedriven`
(`mapRunRedrivenEventDetails` `{"mapRunArn", "redriveCount"}`) where a first
run records `MapRunStarted`. In the map run, a `STANDARD` child that failed,
timed out or was aborted is redriven under its own ARN — its own history gains
an `ExecutionRedriven` — and an `EXPRESS` child is started again from its first
state under the same ARN with a fresh history. Children that succeeded are
left as they were.

## Related

- [Step Functions](../stepfunctions.md)
- [Step Functions limitations](./limitations.md)
- [GetExecutionHistory in the AWS API reference](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetExecutionHistory.html)
