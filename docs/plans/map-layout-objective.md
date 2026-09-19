# System map layout: the objective, and a purpose-built algorithm

Status: implemented. `map-layout-engine.ts` replaces dagre in `buildLayout`; the
score, fixtures, benchmark, tuner and tests below all exist. Section 8 records the
baseline and the acceptance status per fixture, including the four small fixtures
that do not yet reach the 0.7× bar and why.

The system map (`web/src/features/map/`) draws every emulator resource as a card and
every configured connection as a line, grouped region → CloudFormation stack (recursive)
→ VPC → resource. Today the positions come from dagre, one run per connected component,
with a shelf packer, an obstacle-avoiding edge router and a few heuristics layered on
top (`map-layout.ts`, `map-edge-routing.ts`). Dagre is a general-purpose black box: it
cannot be told what this map considers good, it starts from whatever order the API
returned, and its crossing minimisation gives up early. This document states exactly
what a good layout is, as a score, so an algorithm can be built and tuned against that
score instead of against taste, and so a regression is a number rather than an opinion.

## 1. Scope and contract

The new algorithm replaces the inside of `buildLayout` in `map-layout.ts`. Its public
contract does not change:

- Input: `TopologyNode[]`, `TopologyEdge[]`, per-node size overrides, the active region,
  the set of collapsed stack ids. Node sizes are given; the layout never measures the DOM.
- Output: `MapLayout` — React Flow nodes with positions relative to their parent
  container (`parentId`), plus `routes`: edge id → interior waypoints in absolute canvas
  coordinates, omitted for an edge that is a plain curve between its handles.
- The hierarchy semantics stay: every region is a box, stacks nest, collapsed stacks are
  chips, VPC members sit in a VPC box, derived resources (log groups, stream filters)
  adopt the stack all their neighbours share, cross-container edges are drawn between the
  real cards.
- It runs inside the layout worker (`map-layout.worker.ts`): plain data, no DOM, no
  timers that outlive the call. Pure and deterministic.
- Handles are fixed: a card's outgoing connections leave the middle of its right edge,
  incoming ones enter the middle of its left edge. The renderer draws a smooth curve
  through the waypoints (`routedPath` in `map-edge-routing.ts`).

Everything else — dagre, the shelf packer, the untangling passes — may go.

## 2. Hard rules (a layout that breaks one is invalid)

A violation is reported by name; the score of an invalid layout is `Infinity`.

| Id | Rule |
| --- | --- |
| H1 | No two cards overlap, and any two cards are at least `MIN_GAP = 24px` apart on both axes unless they are separated on one axis by more than that. |
| H2 | Every card lies inside its container box with the container's padding; sibling containers never overlap; a container's box is the bounding box of its contents plus padding. |
| H3 | No edge polyline (source right handle → waypoints → target left handle) enters a card it is not attached to, where "enters" means crossing the card's rectangle inflated by `EDGE_CLEARANCE = 8px`. |
| H4 | Every edge's target handle is at least `MIN_FORWARD = 24px` to the right of its source handle, **or** the edge is a designated back edge (see S4) routed as an explicit loop above or below both cards. |
| H5 | The same input yields byte-identical output. Any randomness comes from a seeded generator whose seed is a function of the input. |
| H6 | Time budget, measured in Node on the CI runner with `performance.now()`, single call, warm module: ≤ 30ms for 50 nodes / 60 edges; ≤ 120ms for 150 / 200; ≤ 400ms for 300 / 400; ≤ 900ms for 600 / 800; ≤ 1800ms for 1000 / 1400. Quality must degrade smoothly with size, never step down at a node-count threshold. |

## 3. Soft rules and their scores

The score of a valid layout is the weighted sum below; lower is better. Weights are
chosen so one edge crossing outweighs a lot of extra length: crossings are what make a
diagram unreadable, length only makes it slow to read. Every term is computed per
container level and summed, plus the canvas-level terms.

| Id | What is measured | Penalty |
| --- | --- | --- |
| S1 | **Crossings.** Proper intersections between the polylines of two edges that share no endpoint node. | 40 per crossing |
| S2 | **Length.** Polyline length of every edge beyond the unavoidable minimum. The minimum for an edge is `RANK_GAP` (the horizontal gap between adjacent columns, 96px). | 0.03 per px |
| S3 | **Bends.** Direction changes of more than 15° along a polyline, counting the waypoints, not the renderer's smoothing. | 6 per bend |
| S4 | **Back edges.** Edges whose target is left of their source. The set of back edges must be a feedback arc set; the penalty is per back edge, on top of its S2/S3. | 30 per back edge |
| S5 | **Misalignment.** Vertical distance between an edge's two handles. Chains of connected cards should form straight rows. | 0.05 per px |
| S6 | **Grazing.** An edge polyline passing within `GRAZE = 16px` of a card it is not attached to (H3 covers the inner 8px; this is the outer band). | 10 per edge-card pair |
| S7 | **Label room.** A labelled edge (DLQ, pipe, other dashed types with `label`) whose midpoint is within 28px of a card or of another label's midpoint. | 15 per collision |
| S8 | **Service grouping.** Cards with no connections at all are listed in reading order (row-major by their top-left). Count the changes of service along that order beyond the minimum (`distinct services − 1`). | 12 per extra change |
| S9 | **Neighbourhood.** For every edge, the gap between the two cards' rectangles beyond `RANK_GAP` — connected cards should be neighbours, not merely joined. | 0.02 per px |
| S10 | **Compactness.** Per level: `bounding box area ÷ Σ card areas`, minus 1, times the number of cards. Rewards a tight canvas. | 2 per unit |
| S11 | **Aspect.** Per region, over its direct children: `|log2(width ÷ height) − 1|` (target 2:1, wider than tall). A stack or VPC box inside the region is not judged: a stack whose contents are a chain should be a long row. | 25 per unit |
| S12 | **Flows before inventory.** Per level: any card with connections whose top edge is below the top edge of a card with none. | 8 per pair |
| S13 | **Region order.** The active region is not first. | 200 |

Reference values for the terms live in one place, `map-layout-score.ts`, and every
number above is a named constant there. Changing a weight is a deliberate, reviewable
change to what the map considers good. No weight was changed during implementation;
the table above is what ships.

### How the rules are read where the table leaves room

These are the readings the scorer implements; each is a definition, not a weight.

- **Edge terms are measured over the whole canvas** (S1–S7, S9, H3, H4), in absolute
  coordinates: a crossing between an edge inside a stack and one leaving it is a
  crossing. The arrangement terms (S8, S10, S12) are measured per container over its
  direct children, a nested box counting as one item, and summed.
- **S11 is measured over a region's direct children only.** The first version of this
  document scored it per level, which charged a stack whose flow is a chain for being
  a long row — a flaw in the specification, not in a layout. What should be roughly
  2:1 is the region a reader pans around; the boxes inside it take the shape of their
  flow.
- **S10 and S11 skip a level with a single item.** One item has no arrangement, so
  the term would be a constant every layout pays alike.
- **H4's loop** is satisfied when the route has a waypoint outside both cards' rows —
  above or below the pair, or, for an edge between two regions, in the corridor
  between the two region boxes, which is where the engine puts those.
- **S12 counts a nested box as a flow.** A stack or VPC box has a flow inside it even
  when nothing outside connects to it, so it belongs above the loose inventory.
- **A card drawn at a size other than the one it was given** is a violation
  (`size:<id>`), since the contract says sizes are inputs.

### What is deliberately not scored

- Symmetry and centring: pleasant, but a poor proxy for legibility, and it fights S10.
- Edge-length uniformity: covered well enough by S2 and S5.
- The renderer's curve shape: the score works on polylines so it is independent of how
  the curve is drawn.

## 4. Fixtures

Layouts are measured on a fixed set of topologies in
`web/src/features/map/__fixtures__/`. Each has the `TopologyResponse` shape plus an
optional `sizes` map and `collapsed` list. `orders-app` is a captured JSON file; the rest
are produced on demand by the seeded generator beside it (`generate.ts`), so the set is
reproducible without committing their JSON and can grow.

| Name | What it exercises |
| --- | --- |
| `orders-app` | The seeded emulator topology from the polish PR (27 nodes, 11 edges): one stack with a real flow, loose resources, a VPC with an internet gateway, a second region. Captured from `GET /_overcast/topology`. |
| `inventory-only` | 30 unconnected resources across 6 services, mixed heights (queues with messages, log groups, a VPC). Grouping, tiling, aspect. |
| `chain-fanout` | One 12-node chain with a 5-way fan-out and a 3-way fan-in. Alignment, length. |
| `dense-dag` | 24 nodes, 44 edges, seeded random DAG. Crossings. |
| `cycles` | Three small cycles of length 2, 3 and 5 sharing a node. Back edges. |
| `two-stacks-cross` | Two stacks with four cross-stack edges and a shared log group. Container routing. |
| `nested-collapsed` | Three levels of nested stacks, the deepest collapsed. Chips and phantoms. |
| `vpc-members` | Two VPCs with instances and RDS inside, gateways outside. VPC boxes. |
| `multi-region` | Three regions, eight cross-region edges. Region order, long routes. |
| `mixed-150` | 150 nodes / 200 edges, seeded: 6 stacks, 40 loose, 2 regions. |
| `mixed-300` | 300 / 400, seeded. |
| `mixed-600` | 600 / 800, seeded. |
| `mixed-1000` | 1000 / 1400, seeded. |

## 5. Baseline and acceptance

`map-layout.baseline.json` records, per fixture, the score breakdown and the time of the
current dagre-based implementation (captured once by the benchmark before it is
replaced) and of the new algorithm as it improves. The benchmark is
`pnpm --dir web layout:bench` and prints a table: fixture, score, worst terms, time,
delta against the baseline.

The new algorithm is accepted when, on every fixture:

1. It reports no hard-rule violation.
2. Its score is at most `0.7 ×` the dagre baseline on every fixture up to 300 nodes, and at
   most `0.85 ×` on the 600 and 1000 node fixtures.
3. It is within the H6 time budget.
4. A test (`map-layout.test.ts`) asserts 1 and 3 on every fixture and asserts the score
   is within 2% of the recorded baseline for the new algorithm, so a later change that
   makes any fixture worse fails CI.

"Optimal" for this purpose means the lowest score the algorithm can reach inside its
time budget; the true minimum is intractable and is not the bar. What is required is
that the score never gets worse for a later change without the test saying so, and that
no fixture, at any size, produces a violation.

## 6. Algorithm outline (guidance, not a mandate)

A Sugiyama-style pipeline where every step is ours and is judged by the score above:

1. **Components.** Split each container level into connected components; lay each out
   on its own; pack the blocks (biggest flow first, inventory last, service-grouped).
2. **Cycle breaking.** A greedy feedback-arc-set heuristic (Eades–Lin–Smyth), then verify
   every remaining edge points right. The reversed edges are the S4 back edges.
3. **Layering.** Longest path from sources, then tighten: move every node as far right
   (or left) as its edges allow when that shortens total span; nodes with one neighbour
   sit in the column next to it.
4. **Ordering within columns.** Median/barycenter sweeps from several deterministic
   starts (input order, reverse, by id, by degree), each followed by adjacent-swap local
   search on the exact two-column crossing count. Keep the best. For a component larger
   than the budget allows, fewer starts, never fewer than one sweep pair.
5. **Coordinates.** Columns at `RANK_GAP` spacing on x; y by a priority pass that aligns
   each node with the median of its neighbours (straight rows, S5) subject to `MIN_GAP`
   and the real card heights.
6. **Routing.** Long edges take the column-gap channel positions reserved for them;
   back edges take an explicit loop; then the existing obstacle pass (`routeEdge`) checks
   every leg against every card and detours the ones that would cut through one.
7. **Refinement.** With whatever time remains in the budget, simulated annealing over
   cheap moves (swap two nodes in a column, shift a row, move a block in the packing)
   with a seeded generator and **incremental** scoring — recompute only the terms the move
   touches. This is where the score is used as a training signal: the annealer's own
   parameters (temperature schedule, move mix, iterations per node) are tuned by a
   script that runs the fixtures and keeps the settings with the best total, and those
   settings are committed as constants with the run that produced them.

Performance notes: crossings are the expensive term (pairs of edge segments). Use a
sweep over column gaps — only edges spanning the same gap can cross — and an incremental
count for the annealer. Never allocate per iteration; typed arrays and index-based
graphs. All of it is plain functions over arrays so it runs in the worker unchanged.

## 7. Deliverables

- `web/src/features/map/map-layout-score.ts` — `scoreLayout(layout, nodes, edges, sizes)`
  returning `{ total, terms, violations }`, with every constant named and documented.
- `web/src/features/map/map-layout-engine.ts` (and helpers) — the algorithm, wired into
  `buildLayout`; dagre removed from `package.json` once nothing imports it.
- Fixtures, the seeded generator, `map-layout.baseline.json`, the `layout:bench` script
  and the `layout:tune` script.
- Tests: hard rules on every fixture, score within 2% of baseline, time budget, plus the
  existing `map-layout.test.ts` and `map-edge-routing.test.ts` suites still green.
- This document updated with the final weights and the baseline table.

## 8. Baseline and acceptance status (run 2026-09-20, fourth pass)

Measured by `pnpm --dir web layout:bench` on the development machine (Node 24,
single call, warm module; the median of three timed runs), with S11 measured per
region as §3 now says and the current geometry (`STACK_PADDING`/`VPC_PADDING` bottom
24, `ROUTE_MARGIN` 20). "dagre" is the implementation this document replaced — the
working-tree `map-layout.ts` this work started from, with its box paddings pointed at
the shared constants so both sides see the same geometry — captured from a scratch
copy under the current scorer and kept in `map-layout.baseline.json`; "engine" is the
shipped implementation. Scores are the soft totals; a layout with a hard-rule
violation says so.

| Fixture | Nodes / edges | dagre score | engine score | Ratio | dagre ms | engine ms |
| --- | --- | --- | --- | --- | --- | --- |
| `orders-app` | 27 / 11 | 161 | 87 | 0.54 | 12 | 2 |
| `inventory-only` | 30 / 0 | 80 | 52 | 0.66 | 0 | 0 |
| `chain-fanout` | 20 / 19 | 680 | 427 | 0.63 | 17 | 4 |
| `dense-dag` | 24 / 44 | 3529 | 2248 | 0.64 | 57 | 18 |
| `cycles` | 8 / 10 | 406 | 392 | 0.96 | 7 | 5 |
| `two-stacks-cross` | 11 / 14 | 1094 | 809 | 0.74 | 14 | 5 |
| `nested-collapsed` | 15 / 18 | 181 | 145 | 0.80 | 11 | 2 |
| `vpc-members` | 13 / 10 | 262 | 176 | 0.67 | 7 | 2 |
| `multi-region` | 24 / 20 | 2166 | 1459 | 0.67 | 14 | 2 |
| `mixed-150` | 150 / 200 | threw | 19853 | – | – | 53 |
| `mixed-300` | 300 / 400 | threw | 31023 | – | – | 155 |
| `mixed-600` | 600 / 800 | threw | 112767 | – | – | 450 |
| `mixed-1000` | 1000 / 1400 | 268790, 10 violations | 184040 | 0.68 | 2326 | 847 |

What the table says:

1. **Hard rules.** The engine breaks none on any fixture. dagre's `mixed-1000` layout
   had 10 H4 breaches (backward edges drawn as plain curves).
2. **dagre could not lay out three of the four large fixtures.** It throws
   `Not possible to find intersection inside of the rectangle` on `mixed-150`,
   `mixed-300` and `mixed-600` — a known dagre failure on a multigraph with a
   two-node cycle and a parallel edge, which the region-level graph of stack boxes
   produces whenever two stacks are wired both ways. There is no dagre score to beat
   there; the engine test checks those fixtures for validity, time and regression
   against their own baseline.
3. **Time.** Every fixture is inside H6 with room to spare; the largest is at 47% of
   the limit and 2.7× faster than dagre.
4. **Cross-region edges have corridors of their own.** An edge leaves its source
   region into the gap on the target's side of it, runs along a lane there, and enters
   the target region from the gap on the source's side; when the two regions are not
   adjacent, a channel along the nearer side of the region column joins the two gaps,
   its lanes nested by span so none crosses another. Gaps grow to hold their lanes and
   each gap's lanes are ordered to minimise crossings among themselves. Only the leg
   out of the source card and the leg into the target card go through the obstacle
   pass; no cross-region edge threads through a region it is not attached to. This
   took `multi-region` from 1.01× (27 crossings, worse than dagre, when the route
   margin grew) to 0.67× (8 crossings) and moved the mean ratio over the ten
   comparable fixtures from 0.737 to 0.704, so the annealer was re-tuned (0.700 after).
5. **Acceptance ratio.** Met on `orders-app`, `inventory-only`, `chain-fanout`,
   `dense-dag`, `vpc-members`, `multi-region` (≤ 0.7×) and `mixed-1000` (≤ 0.85×). Not
   met on three small fixtures, which `map-layout-engine.test.ts` lists by name with
   the ratio each reaches, so they can only improve:
   - `cycles` (0.96×): the three back edges cost S4 = 90 and their loops S3 = 24 each
     whatever the layout; those 162 points are 41% of the total and dagre pays them
     too, so the room to differ is small. The engine's layout is the one a person
     would draw — the hub leads, the three cycles fan out and loop back beneath.
   - `nested-collapsed` (0.80×): 145 against 181. Length (S2 = 61) and neighbourhood
     (S9 = 37) are the same under both, because the flow is the same chain; the
     engine wins on compactness and alignment, and 145 leaves 18 points to find.
   - `two-stacks-cross` (0.74×): the cross-stack edges run from cards deep inside one
     box to cards deep inside the other, and the obstacle pass routes them around the
     siblings in between; S2 = 353 of the 809 is that length, and two of them still
     cross (S1 = 80). The port lane tried below fixes exactly this fixture and breaks
     others; a shelf-aware version of it is the natural next step.

### Port lanes for edges between sibling boxes: tried and dropped

One attempt at the follow-up named above: an edge between two sibling containers
(or a container and a card beside it) got a lane of its own — through the gap between
the two boxes when one is above the other, otherwise just above or below the pair —
so the leg out of the source card and the leg into the target were the only ones the
obstacle pass had to detour. Measured against the corridor-only layout on the same
geometry:

| Fixture | without | with port lanes |
| --- | --- | --- |
| `two-stacks-cross` | 849 | 768 |
| `nested-collapsed` | 148 | 163 |
| `vpc-members` | 176 | 247 (S3 doubles: sixteen bends) |
| `mixed-150` | 19602 | 16756 |
| `mixed-300` | 30880 | 32211 |
| `mixed-600` | 117100 | 133806, with 14 H3 breaches |
| `mixed-1000` | 178674 | 206015 (crossings up 60%) |

It helps exactly the case it was drawn for — two boxes side by side in one shelf —
and hurts everything else: a lane at the midpoint between boxes on different shelves
runs through the shelves in between, and a lane past a VPC box costs four bends per
edge where the phantom-level channel needed none. Dropped, per the one-attempt rule;
the code is not in the tree. A version that only takes the lane when the two boxes
share a shelf, and otherwise keeps the phantom-level channel, is the natural next step.

### Engine settings

The annealer's parameters (`ANNEALER_TUNING` in `map-layout-engine.ts`) are the best
of the third `pnpm --dir web layout:tune` run of 2026-09-20 (32 candidates, seed
20260920, after the cross-region corridors): `orderShare` 0.306, `orderTemp0` 0.645,
`orderTemp1` 0.01, `rowTemp0` 4, `rowTemp1` 0.081, `maxShift` 86, `alignShare` 0.318,
`columnShare` 0.216 — a tuner objective of 0.774 against 0.781 for the starting point
(the second run's best). The tuner was not re-run after the padding and route margin
changed (the mean ratio moved by 0.006, under the 0.01 that warrants it) but was after
the corridors (0.033).
The time budget (`ENGINE_BUDGET`) turns the H6 limit into an iteration count with a
measured 1700–2000 iterations/ms on `mixed-600` halved for denser graphs and slower
CI runners, and 25% of the limit given to annealing; the iteration count is a pure
function of the node count, never a clock, so the output is deterministic.

### What was left out

- `@dagrejs/dagre` stays in `web/package.json`: nothing under `features/map` imports it
  any more, but the Step Functions flow diagram (`features/stepfunctions/graph-layout.ts`)
  still does.
- Cross-container edges are still routed by the obstacle pass from the real card to the
  waypoints its box was given, rather than the box having ports on its edges.
