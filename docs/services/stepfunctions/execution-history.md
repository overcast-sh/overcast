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
| Distributed Map | `MapStateStarted`, `MapRunStarted` (`mapRunArn`), `MapRunSucceeded` or `MapRunFailed`, then `MapStateSucceeded` or `MapStateFailed` |

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

## Related

- [Step Functions](../stepfunctions.md)
- [Step Functions limitations](./limitations.md)
- [GetExecutionHistory in the AWS API reference](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetExecutionHistory.html)
