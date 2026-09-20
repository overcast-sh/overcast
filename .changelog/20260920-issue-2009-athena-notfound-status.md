* [athena] not-found errors answer HTTP 400, the status AWS JSON 1.1 gives `InvalidRequestException`, instead of 404
+ [athena] `StopQueryExecution` is implemented: it answers an empty document for a known query id and `InvalidRequestException` for an unknown one
