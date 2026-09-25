* [athena] a query's result is no longer held whole in memory while it is written.
  rows go to the result object and to `GetQueryResults` as the engine returns them.
+ [config] `ATHENA_MAX_RESULT_BYTES` (default `1g`) fails a query whose result is larger, keeping none of it.
