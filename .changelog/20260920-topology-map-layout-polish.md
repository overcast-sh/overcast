~ [web] the system map has its own layout engine, built and tuned against a written objective instead of dagre
  the objective (no overlaps, no wires through cards, fewest crossings, shortest wires, straight rows), its weights and its fixtures are in docs/plans/map-layout-objective.md
  a test fails if any fixture's score regresses; dagre could not lay out three of the four large fixtures at all
  the engine handles 1,000 resources in under a second with no rule broken
~ [web] the system map lays out each connected flow on its own, packs the flows into a wide canvas and tiles unconnected resources by service beneath them
  every connection is routed around the nodes it is not attached to — across stacks, VPCs and regions — and drawn as one smooth curve through its waypoints
  a resource card's box now matches its rendered height, so connections meet cards on their midline and rows sit closer together
  a function's log group and stream filter are drawn inside the function's stack
+ [web] hovering a node on the system map dims everything except that node, its connections and its neighbours
  hovering a legend entry does the same for one connection type
~ [web] the system map's legend lists only the connection types present, and zooming out fades the lists inside nodes
  message, stream and instance lists fade below 0.6× zoom so the map reads as boxes and wires; edge labels hide
~ [web] system map wires are quieter when idle and rounder at their turns, and the region badge sticks to the top edge
  the map can now zoom out far enough to fit a large topology (React Flow's default floor was 0.5×)
* [web] the system map no longer sends an SQS peek request for every non-queue node, and shows its spinner until the first layout is ready
