*! [eventbridge] `DescribeEventBus` raises `ResourceNotFoundException` for a bus that does not exist, as AWS does, rather than inventing one.
  the `default` bus still always exists; a bus named by ARN is looked up by its name.
  migration: create the bus with `CreateEventBus` (or in the template) before describing it, and treat `ResourceNotFoundException` as "no such bus".
* [eventbridge] `ListEventBuses` honours `NamePrefix`, `Limit` and `NextToken` instead of always returning every bus on one page.
  an omitted `Limit` pages at 100, the modeled cap; a `Limit` outside 1..100 or an unrecognised `NextToken` is `ValidationException`.
