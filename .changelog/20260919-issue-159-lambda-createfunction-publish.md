+ [lambda] `CreateFunction` with `Publish: true` publishes version 1 in the same call and answers with that version, as on AWS
  every FunctionConfiguration now carries `Version` (`$LATEST` for the unpublished function), which the SDKs model and AWS always returns
  `PublishTo: LATEST_PUBLISHED` (the `$LATEST.PUBLISHED` version of Lambda Managed Instances) still returns 501
